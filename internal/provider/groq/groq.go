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

	// 1. Transcribe source speech with explicit source language hint for accuracy
	srcText, err := s.callGroqTranscription(wavData, s.direction.Src)
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

	// 2. High-accuracy Translation using Qwen 27B / GPT-OSS
	translatedText, err := s.callGroqChatTranslate(srcText, s.direction.Src, s.direction.Dst)
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

	// 3. High-Fidelity Neural Speech Synthesis (Microsoft Edge Neural Voice)
	if translatedText != "" {
		pcmAudio := s.synthesizeNeuralVoice(translatedText, s.direction.Dst)
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

func (s *Session) synthesizeNeuralVoice(text, lang string) []byte {
	if text == "" {
		return nil
	}

	// Choose neural voice based on target language
	voice := "en-US-AriaNeural"
	switch lang {
	case "ml":
		voice = "ml-IN-SobhanaNeural"
	case "hi":
		voice = "hi-IN-SwaraNeural"
	case "es":
		voice = "es-ES-ElviraNeural"
	case "fr":
		voice = "fr-FR-DeniseNeural"
	case "de":
		voice = "de-DE-KatjaNeural"
	case "zh":
		voice = "zh-CN-XiaoxiaoNeural"
	case "ja":
		voice = "ja-JP-NanamiNeural"
	case "ar":
		voice = "ar-SA-ZariyahNeural"
	default:
		voice = "en-US-AriaNeural"
	}

	// Run Edge TTS pipeline with ffmpeg to generate 16kHz mono PCM16
	// python3 -m edge_tts --voice <voice> --text <text> --write-media - | ffmpeg -i - -f s16le -ar 16000 -ac 1 -
	cmd1 := exec.CommandContext(s.ctx, "python3", "-m", "edge_tts", "--voice", voice, "--text", text, "--write-media", "-")
	cmd2 := exec.CommandContext(s.ctx, "ffmpeg", "-y", "-i", "-", "-f", "s16le", "-ar", "16000", "-ac", "1", "-")

	r, w := io.Pipe()
	cmd1.Stdout = w
	cmd2.Stdin = r

	var out bytes.Buffer
	cmd2.Stdout = &out

	if err := cmd1.Start(); err != nil {
		return s.fallbackEspeak(text, lang)
	}
	if err := cmd2.Start(); err != nil {
		_ = cmd1.Process.Kill()
		return s.fallbackEspeak(text, lang)
	}

	go func() {
		_ = cmd1.Wait()
		_ = w.Close()
	}()

	if err := cmd2.Wait(); err != nil || out.Len() == 0 {
		return s.fallbackEspeak(text, lang)
	}

	return out.Bytes()
}

func (s *Session) fallbackEspeak(text, lang string) []byte {
	voice := "en-us"
	if lang != "en" && lang != "" {
		voice = lang
	}
	cmd1 := exec.CommandContext(s.ctx, "espeak-ng", "-v", voice, "-s", "145", "--stdout", text)
	cmd2 := exec.CommandContext(s.ctx, "ffmpeg", "-y", "-i", "-", "-f", "s16le", "-ar", "16000", "-ac", "1", "-")

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

func (s *Session) callGroqTranscription(wavBytes []byte, srcLang string) (string, error) {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavBytes); err != nil {
		return "", err
	}

	_ = writer.WriteField("model", "whisper-large-v3")
	_ = writer.WriteField("response_format", "json")
	_ = writer.WriteField("temperature", "0.0")

	// Set language parameter if provided to constrain decoder vocabulary
	if srcLang != "" && srcLang != "auto" {
		_ = writer.WriteField("language", srcLang)
	}

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

func (s *Session) callGroqChatTranslate(text, srcLang, dstLang string) (string, error) {
	systemPrompt := fmt.Sprintf(`You are an expert bilingual interpreter. Your task is to translate spoken speech from '%s' into natural, fluent, idiomatic '%s'.
Guidelines:
1. Translate accurately, preserving the speaker's original intent, conversational tone, and meaning.
2. If the user speech is colloquial, convert it into natural everyday language.
3. Output ONLY the pure translated sentence.
4. Do NOT output notes, explanations, phonetic guides, or quotation marks.`, srcLang, dstLang)

	reqBody := map[string]any{
		"model": "qwen/qwen3.8-27b",
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": systemPrompt,
			},
			{
				"role":    "user",
				"content": text,
			},
		},
		"temperature": 0.1,
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
		// Fallback to gpt-oss-120b if qwen fails
		return s.callFallbackModel(text, systemPrompt)
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
		return "", errors.New("empty translation choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

func (s *Session) callFallbackModel(text, systemPrompt string) (string, error) {
	reqBody := map[string]any{
		"model": "openai/gpt-oss-120b",
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": systemPrompt,
			},
			{
				"role":    "user",
				"content": text,
			},
		},
		"temperature": 0.1,
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
		return "", fmt.Errorf("groq fallback translation failed (%d): %s", res.StatusCode, string(respBytes))
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
		return "", errors.New("empty translation response")
	}
	return parsed.Choices[0].Message.Content, nil
}
