package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"

	"parlex/internal/audio"
	"parlex/internal/provider"
)

type Config struct {
	APIKey string
	Model  string // default: "whisper-large-v3"
}

type Provider struct {
	cfg Config
}

func New(cfg Config) *Provider {
	if cfg.Model == "" {
		cfg.Model = "whisper-large-v3"
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string {
	return "groq"
}

func (p *Provider) Supports(d provider.Direction) bool {
	return d.Src != "" && d.Dst != ""
}

func (p *Provider) Open(ctx context.Context, d provider.Direction) (provider.Session, error) {
	sessCtx, cancel := context.WithCancel(ctx)
	sess := &Session{
		cfg:        p.cfg,
		direction:  d,
		ctx:        sessCtx,
		cancel:     cancel,
		eventsCh:   make(chan provider.Event, 64),
		audioQueue: make(chan []byte, 128),
		audioBuf:   make([]byte, 0, 96000),
		client: &http.Client{
			Timeout: 25 * time.Second,
		},
	}
	go sess.run()
	return sess, nil
}

type Session struct {
	cfg        Config
	direction  provider.Direction
	ctx        context.Context
	cancel     context.CancelFunc
	eventsCh   chan provider.Event
	audioQueue chan []byte
	mu         sync.Mutex
	audioBuf   []byte
	closed     bool
	client     *http.Client
}

func (s *Session) InputFormat() provider.AudioFormat {
	return provider.AudioFormat{
		SampleRate: 16000,
		Channels:   1,
		FrameMs:    20,
	}
}

func (s *Session) OutputFormat() provider.AudioFormat {
	return provider.AudioFormat{
		SampleRate: 16000,
		Channels:   1,
		FrameMs:    20,
	}
}

func (s *Session) SendAudio(ctx context.Context, pcm []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("groq session input closed")
	}
	s.mu.Unlock()

	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case s.audioQueue <- pcm:
		return nil
	default:
		return provider.ErrQueueFull
	}
}

func (s *Session) CloseInput(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	close(s.audioQueue)
	return nil
}

func (s *Session) Events() <-chan provider.Event {
	return s.eventsCh
}

func (s *Session) Abort() {
	s.cancel()
}

func (s *Session) run() {
	defer close(s.eventsCh)

	// Ingest audio frames from device
	for {
		select {
		case <-s.ctx.Done():
			return
		case pcm, ok := <-s.audioQueue:
			if !ok {
				goto processAudio
			}
			s.mu.Lock()
			s.audioBuf = append(s.audioBuf, pcm...)
			s.mu.Unlock()
		}
	}

processAudio:
	s.mu.Lock()
	audioLen := len(s.audioBuf)
	s.mu.Unlock()

	if audioLen < 1600 { // less than 50ms of audio
		select {
		case <-s.ctx.Done():
		case s.eventsCh <- provider.Event{Kind: provider.EventDone}:
		}
		return
	}

	wavData := audio.EncodeWAV(s.audioBuf, 16000)
	var translatedText string

	// If translating to English, Groq's audio/translations translates speech directly to English
	if s.direction.Dst == "en" {
		var err error
		translatedText, err = s.callGroqTranslation(wavData)
		if err != nil {
			s.eventsCh <- provider.Event{
				Kind: provider.EventFailed,
				Err:  err,
				Text: err.Error(),
			}
			return
		}

		s.eventsCh <- provider.Event{
			Kind: provider.EventTargetText,
			Text: translatedText,
		}
	} else {
		// 1. Transcribe source speech
		srcText, err := s.callGroqTranscription(wavData)
		if err != nil {
			s.eventsCh <- provider.Event{
				Kind: provider.EventFailed,
				Err:  err,
				Text: err.Error(),
			}
			return
		}

		s.eventsCh <- provider.Event{
			Kind: provider.EventSourceText,
			Text: srcText,
		}

		// 2. Translate with LLaMA 3.3 70B
		var err2 error
		translatedText, err2 = s.callGroqChatTranslate(srcText, s.direction.Dst)
		if err2 != nil {
			s.eventsCh <- provider.Event{
				Kind: provider.EventFailed,
				Err:  err2,
				Text: err2.Error(),
			}
			return
		}

		s.eventsCh <- provider.Event{
			Kind: provider.EventTargetText,
			Text: translatedText,
		}
	}

	// 3. Synthesize English/Target Speech Audio (16kHz mono PCM16)
	if translatedText != "" {
		pcmAudio := s.synthesizeSpeechAudio(translatedText, s.direction.Dst)
		if len(pcmAudio) > 0 {
			chunkSize := 640 // 20ms @ 16kHz
			for i := 0; i < len(pcmAudio); i += chunkSize {
				end := i + chunkSize
				if end > len(pcmAudio) {
					end = len(pcmAudio)
				}
				select {
				case <-s.ctx.Done():
					return
				case s.eventsCh <- provider.Event{
					Kind: provider.EventAudioOut,
					PCM:  pcmAudio[i:end],
				}:
				}
			}
		}
	}

	// Emit Done event
	select {
	case <-s.ctx.Done():
	case s.eventsCh <- provider.Event{Kind: provider.EventDone}:
	}
}

func (s *Session) synthesizeSpeechAudio(text, lang string) []byte {
	if text == "" {
		return nil
	}
	// Try Google TTS first (natural voice)
	if pcm := s.tryGoogleTTS(text, lang); len(pcm) > 0 {
		return pcm
	}
	// Fallback to local espeak-ng / espeak
	if pcm := s.tryEspeakTTS(text, lang); len(pcm) > 0 {
		return pcm
	}
	return nil
}

func (s *Session) tryGoogleTTS(text, lang string) []byte {
	targetLang := "en"
	if lang != "" {
		targetLang = lang
	}
	u := fmt.Sprintf("https://translate.google.com/translate_tts?ie=UTF-8&tl=%s&client=tw-ob&q=%s",
		url.QueryEscape(targetLang), url.QueryEscape(text))

	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	res, err := s.client.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		return nil
	}
	defer res.Body.Close()

	// Convert MP3 to 16kHz mono PCM16 via ffmpeg
	cmd := exec.CommandContext(s.ctx, "ffmpeg", "-i", "-", "-f", "s16le", "-ar", "16000", "-ac", "1", "-")
	cmd.Stdin = res.Body
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	return out
}

func (s *Session) tryEspeakTTS(text, lang string) []byte {
	voice := "en-us"
	if lang != "en" && lang != "" {
		voice = lang
	}
	cmd1 := exec.CommandContext(s.ctx, "espeak-ng", "-v", voice, "-s", "145", "--stdout", text)
	cmd2 := exec.CommandContext(s.ctx, "ffmpeg", "-i", "-", "-f", "s16le", "-ar", "16000", "-ac", "1", "-")

	r, w := io.Pipe()
	cmd1.Stdout = w
	cmd2.Stdin = r

	var out bytes.Buffer
	cmd2.Stdout = &out

	if err := cmd1.Start(); err != nil {
		return nil
	}
	if err := cmd2.Start(); err != nil {
		return nil
	}
	go func() {
		_ = cmd1.Wait()
		_ = w.Close()
	}()
	_ = cmd2.Wait()

	return out.Bytes()
}

func (s *Session) callGroqTranslation(wavBytes []byte) (string, error) {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavBytes); err != nil {
		return "", err
	}

	_ = writer.WriteField("model", s.cfg.Model)
	_ = writer.WriteField("response_format", "json")
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, "https://api.groq.com/openai/v1/audio/translations", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	respBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq audio translation failed (%d): %s", res.StatusCode, string(respBytes))
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", err
	}
	return parsed.Text, nil
}

func (s *Session) callGroqTranscription(wavBytes []byte) (string, error) {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavBytes); err != nil {
		return "", err
	}

	_ = writer.WriteField("model", s.cfg.Model)
	_ = writer.WriteField("response_format", "json")
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, "https://api.groq.com/openai/v1/audio/transcriptions", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	respBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq audio transcription failed (%d): %s", res.StatusCode, string(respBytes))
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", err
	}
	return parsed.Text, nil
}

func (s *Session) callGroqChatTranslate(text, dstLang string) (string, error) {
	reqBody := map[string]any{
		"model": "llama-3.3-70b-versatile",
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": fmt.Sprintf("You are an expert real-time translator. Translate the user speech text accurately into language code '%s'. Output ONLY the translated text without commentary, quotes, or notes.", dstLang),
			},
			{
				"role":    "user",
				"content": text,
			},
		},
		"temperature": 0.2,
	}

	jsonBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(jsonBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	respBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq chat translation failed (%d): %s", res.StatusCode, string(respBytes))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", err
	}

	if len(parsed.Choices) == 0 {
		return "", errors.New("empty response from translation model")
	}
	return parsed.Choices[0].Message.Content, nil
}
