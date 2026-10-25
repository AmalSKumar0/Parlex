/**
 * ESP32-S3 Hardware Translator Demo & Telemetry Driver
 * Parlex Web Client
 */

// State Management
const state = {
  serverUrl: 'http://localhost:8080',
  adminApiKey: 'change-this-to-a-secure-random-admin-secret-key',
  deviceId: null,
  deviceSecret: null,
  claimCode: null,
  streamToken: null,
  ws: null,
  connectionState: 'DISCONNECTED', // DISCONNECTED, CONNECTED, IDLE, STARTING, LISTENING, DRAINING, PLAYING
  
  // Hardware Settings
  settings: {
    version: 1,
    buttonA: { src: 'en', dst: 'es' },
    buttonB: { src: 'es', dst: 'en' },
    volume: 70,
    captions: true,
    ledLevel: 50
  },
  
  // Audio Pipeline
  audioSource: 'mic', // 'mic' or 'synth'
  audioCtx: null,
  micStream: null,
  micProcessor: null,
  isRecording: false,
  
  // Current Turn
  currentTurnId: 1,
  activeButton: null,
  uplinkSeq: 0,
  downlinkSeq: 0,
  turnStartTime: 0,
  firstAudioTime: 0,
  
  // Downlink Audio Playback Queue
  playbackQueue: [],
  isPlayingDownlink: false,
  nextPlayTime: 0,
  
  // Telemetry Counters
  stats: {
    uplinkFrames: 0,
    downlinkFrames: 0,
    firstAudioMs: null,
    totalLatencyMs: null
  }
};

// DOM Elements
const el = {
  // Connection / Config
  serverUrl: document.getElementById('serverUrl'),
  adminApiKey: document.getElementById('adminApiKey'),
  btnOneClickSetup: document.getElementById('btnOneClickSetup'),
  btnDisconnect: document.getElementById('btnDisconnect'),
  btnClaimDevice: document.getElementById('btnClaimDevice'),
  connPill: document.getElementById('connPill'),
  connStatusText: document.getElementById('connStatusText'),
  displayDevId: document.getElementById('displayDevId'),
  displaySecret: document.getElementById('displaySecret'),
  displayClaimCode: document.getElementById('displayClaimCode'),
  displayToken: document.getElementById('displayToken'),
  
  // Device Hardware
  deviceLed: document.getElementById('deviceLed'),
  ledLabel: document.getElementById('ledLabel'),
  oledScreen: document.getElementById('oledScreen'),
  oledState: document.getElementById('oledState'),
  oledLangs: document.getElementById('oledLangs'),
  oledSrcLang: document.getElementById('oledSrcLang'),
  oledDstLang: document.getElementById('oledDstLang'),
  oledCanvas: document.getElementById('oledCanvas'),
  oledCaption: document.getElementById('oledCaption'),
  oledVolume: document.getElementById('oledVolume'),
  oledTurnId: document.getElementById('oledTurnId'),
  btnA: document.getElementById('btnA'),
  btnB: document.getElementById('btnB'),
  btnADir: document.getElementById('btnADir'),
  btnBDir: document.getElementById('btnBDir'),
  btnCancelTurn: document.getElementById('btnCancelTurn'),
  btnPowerCycle: document.getElementById('btnPowerCycle'),
  
  // Settings Form
  etagBadge: document.getElementById('etagBadge'),
  selBtnASrc: document.getElementById('selBtnASrc'),
  selBtnADst: document.getElementById('selBtnADst'),
  selBtnBSrc: document.getElementById('selBtnBSrc'),
  selBtnBDst: document.getElementById('selBtnBDst'),
  rangeVolume: document.getElementById('rangeVolume'),
  rangeLed: document.getElementById('rangeLed'),
  lblVolume: document.getElementById('lblVolume'),
  lblLed: document.getElementById('lblLed'),
  chkCaptions: document.getElementById('chkCaptions'),
  btnSaveSettings: document.getElementById('btnSaveSettings'),
  btnRefreshSettings: document.getElementById('btnRefreshSettings'),
  btnQueryUsage: document.getElementById('btnQueryUsage'),
  
  // Audio Controls
  radMic: document.getElementById('radMic'),
  radSynth: document.getElementById('radSynth'),
  audioCtxStatus: document.getElementById('audioCtxStatus'),
  playbackStatus: document.getElementById('playbackStatus'),
  btnTestSpeaker: document.getElementById('btnTestSpeaker'),
  
  // Telemetry
  statUplinkFrames: document.getElementById('statUplinkFrames'),
  statDownlinkFrames: document.getElementById('statDownlinkFrames'),
  statFirstAudio: document.getElementById('statFirstAudio'),
  statTotalLatency: document.getElementById('statTotalLatency'),
  consoleLog: document.getElementById('consoleLog'),
  btnClearLogs: document.getElementById('btnClearLogs')
};

// Canvas 2D Context for OLED waveform
const canvasCtx = el.oledCanvas.getContext('2d');

/* ==========================================================
   Logging & UI Helpers
   ========================================================== */

function log(type, message) {
  const entry = document.createElement('div');
  entry.className = `log-entry ${type}`;
  const now = new Date().toISOString().substring(11, 23);
  entry.textContent = `[${now}] ${message}`;
  el.consoleLog.appendChild(entry);
  el.consoleLog.scrollTop = el.consoleLog.scrollHeight;
}

function updateConnectionUI(stateName) {
  state.connectionState = stateName;
  el.connStatusText.textContent = stateName;
  el.oledState.textContent = stateName;

  const dot = el.connPill.querySelector('.status-dot');
  dot.className = 'status-dot';
  el.deviceLed.className = 'rgb-led';

  switch (stateName) {
    case 'CONNECTED':
    case 'IDLE':
      dot.classList.add('connected');
      el.deviceLed.classList.add('idle');
      el.ledLabel.textContent = 'STANDBY';
      break;
    case 'STARTING':
    case 'LISTENING':
      dot.classList.add('listening');
      el.deviceLed.classList.add('listening');
      el.ledLabel.textContent = 'MIC ON';
      break;
    case 'DRAINING':
      dot.classList.add('listening');
      el.deviceLed.classList.add('draining');
      el.ledLabel.textContent = 'PROCESSING';
      break;
    case 'PLAYING':
      dot.classList.add('playing');
      el.deviceLed.classList.add('playing');
      el.ledLabel.textContent = 'SPEAKER';
      break;
    case 'ERROR':
      dot.classList.add('disconnected');
      el.deviceLed.classList.add('error');
      el.ledLabel.textContent = 'ERROR';
      break;
    default:
      dot.classList.add('disconnected');
      el.ledLabel.textContent = 'OFF';
  }
}

function updateSettingsDisplay() {
  el.oledSrcLang.textContent = state.settings.buttonA.src.toUpperCase();
  el.oledDstLang.textContent = state.settings.buttonA.dst.toUpperCase();
  el.btnADir.textContent = `${state.settings.buttonA.src.toUpperCase()} ➜ ${state.settings.buttonA.dst.toUpperCase()}`;
  el.btnBDir.textContent = `${state.settings.buttonB.src.toUpperCase()} ➜ ${state.settings.buttonB.dst.toUpperCase()}`;
  el.oledVolume.textContent = `VOL: ${state.settings.volume}%`;
  el.lblVolume.textContent = `${state.settings.volume}%`;
  el.lblLed.textContent = `${state.settings.ledLevel}%`;
  el.rangeVolume.value = state.settings.volume;
  el.rangeLed.value = state.settings.ledLevel;
  el.chkCaptions.checked = state.settings.captions;
  el.etagBadge.textContent = `ETag v${state.settings.version}`;

  el.selBtnASrc.value = state.settings.buttonA.src;
  el.selBtnADst.value = state.settings.buttonA.dst;
  el.selBtnBSrc.value = state.settings.buttonB.src;
  el.selBtnBDst.value = state.settings.buttonB.dst;
}

/* ==========================================================
   REST API Calls (AJAX Fetch)
   ========================================================== */

async function apiRequest(endpoint, options = {}) {
  const url = `${el.serverUrl.value.trim()}${endpoint}`;
  try {
    const res = await fetch(url, options);
    const contentType = res.headers.get('content-type') || '';
    let body = null;
    if (contentType.includes('application/json') || contentType.includes('problem+json')) {
      body = await res.json();
    } else {
      body = await res.text();
    }
    return { status: res.status, ok: res.ok, headers: res.headers, body };
  } catch (err) {
    log('err', `Fetch error on ${endpoint}: ${err.message}`);
    throw err;
  }
}

// 1. Provision Device
async function provisionDevice() {
  log('info', 'Provisioning new device via POST /v1/admin/devices...');
  const res = await apiRequest('/v1/admin/devices', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Admin-Key': el.adminApiKey.value.trim()
    },
    body: JSON.stringify({ name: 'ESP32-S3-DemoDevice' })
  });

  if (!res.ok) {
    log('err', `Device provisioning failed (${res.status}): ${JSON.stringify(res.body)}`);
    return false;
  }

  state.deviceId = res.body.device_id;
  state.deviceSecret = res.body.device_secret;
  state.claimCode = res.body.claim_code;

  el.displayDevId.textContent = state.deviceId;
  el.displaySecret.textContent = state.deviceSecret.substring(0, 16) + '...';
  el.displayClaimCode.textContent = state.claimCode || 'CLAIMED';
  log('success', `Device provisioned: ID=${state.deviceId}`);
  return true;
}

// 2. Claim Device
async function claimDevice() {
  if (!state.claimCode) {
    alert('No claim code available. Provision a device first.');
    return;
  }
  log('info', `Claiming device with code ${state.claimCode}...`);
  const res = await apiRequest('/v1/devices/claim', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ claim_code: state.claimCode })
  });

  if (res.ok) {
    log('success', `Device claimed successfully! Owner token received.`);
    el.displayClaimCode.textContent = 'CLAIMED (Active)';
  } else {
    log('err', `Claim failed (${res.status}): ${JSON.stringify(res.body)}`);
  }
}

// 3. Issue Device Stream Token
async function fetchStreamToken() {
  log('info', 'Exchanging device credentials for stream token via POST /v1/auth/device-token...');
  const res = await apiRequest('/v1/auth/device-token', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      device_id: state.deviceId,
      device_secret: state.deviceSecret
    })
  });

  if (!res.ok) {
    log('err', `Stream token exchange failed (${res.status}): ${JSON.stringify(res.body)}`);
    return false;
  }

  state.streamToken = res.body.token;
  el.displayToken.textContent = state.streamToken.substring(0, 24) + '... (valid 15m)';
  log('success', 'Stream token acquired successfully.');
  return true;
}

// 4. Fetch Settings (GET)
async function fetchSettings() {
  if (!state.deviceId || !state.streamToken) return;
  const res = await apiRequest(`/v1/devices/${state.deviceId}/settings`, {
    headers: { 'Authorization': `Bearer ${state.streamToken}` }
  });

  if (res.ok) {
    const s = res.body;
    state.settings.version = s.version;
    state.settings.buttonA = s.button_a;
    state.settings.buttonB = s.button_b;
    state.settings.volume = s.volume;
    state.settings.captions = s.captions;
    state.settings.ledLevel = s.led_level;
    updateSettingsDisplay();
    log('info', `Settings loaded from server (version ${s.version}).`);
  }
}

// 5. Update Settings (PATCH with ETag / If-Match)
async function saveSettings() {
  if (!state.deviceId || !state.streamToken) {
    alert('Please provision and authenticate device first.');
    return;
  }

  const patchBody = {
    button_a: {
      source: el.selBtnASrc.value,
      target: el.selBtnADst.value
    },
    button_b: {
      source: el.selBtnBSrc.value,
      target: el.selBtnBDst.value
    },
    volume: parseInt(el.rangeVolume.value, 10),
    captions: el.chkCaptions.checked,
    led_level: parseInt(el.rangeLed.value, 10)
  };

  log('info', `Saving settings with If-Match: "${state.settings.version}"...`);
  const res = await apiRequest(`/v1/devices/${state.deviceId}/settings`, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${state.streamToken}`,
      'If-Match': `"${state.settings.version}"`
    },
    body: JSON.stringify(patchBody)
  });

  if (res.ok) {
    const s = res.body;
    state.settings.version = s.version;
    state.settings.buttonA = s.button_a;
    state.settings.buttonB = s.button_b;
    state.settings.volume = s.volume;
    state.settings.captions = s.captions;
    state.settings.ledLevel = s.led_level;
    updateSettingsDisplay();
    log('success', `Settings updated to version ${s.version}!`);
  } else if (res.status === 412) {
    log('warn', 'ETag conflict (412 Precondition Failed). Refreshing latest settings...');
    await fetchSettings();
  } else {
    log('err', `Settings update failed (${res.status}): ${JSON.stringify(res.body)}`);
  }
}

// 6. Query Usage & Quota
async function queryUsage() {
  if (!state.deviceId || !state.streamToken) return;
  const res = await apiRequest(`/v1/devices/${state.deviceId}/usage`, {
    headers: { 'Authorization': `Bearer ${state.streamToken}` }
  });

  if (res.ok) {
    log('info', `Daily Quota Usage: ${JSON.stringify(res.body)}`);
    alert(`Usage Data:\n${JSON.stringify(res.body, null, 2)}`);
  }
}

/* ==========================================================
   WebSocket Device Protocol (`/v1/stream`)
   ========================================================== */

function connectWebSocket() {
  if (!state.streamToken) return;
  if (state.ws) {
    state.ws.close();
    state.ws = null;
  }

  const base = el.serverUrl.value.trim().replace(/^http/, 'ws');
  const wsUrl = `${base}/v1/stream`;

  log('info', `Connecting WebSocket: ${wsUrl} (subprotocol: translator.v1)...`);
  updateConnectionUI('CONNECTING');

  // Browser WebSocket doesn't support custom headers, but supports protocols.
  // Note: For browsers, the server accepts the token in query parameter or subprotocol fallback,
  // or via Sec-WebSocket-Protocol. Parlex supports subprotocol `translator.v1`.
  // To pass token, we can encode as subprotocol array item or query token.
  const ws = new WebSocket(`${wsUrl}?token=${encodeURIComponent(state.streamToken)}`, ['translator.v1']);
  ws.binaryType = 'arraybuffer';
  state.ws = ws;

  ws.onopen = () => {
    log('success', 'WebSocket connected! Sending hello handshake...');
    updateConnectionUI('CONNECTED');
    
    // Send hello message
    const hello = {
      t: 'hello',
      fw: 'esp32s3-demo-v1.0.0',
      proto: 1,
      codecs: ['pcm16'],
      battery: 98
    };
    sendJson(hello);
  };

  ws.onmessage = (event) => {
    if (typeof event.data === 'string') {
      handleTextControlMessage(JSON.parse(event.data));
    } else if (event.data instanceof ArrayBuffer) {
      handleBinaryAudioDownlink(event.data);
    }
  };

  ws.onclose = (event) => {
    log('warn', `WebSocket closed (code: ${event.code}, reason: ${event.reason || 'none'})`);
    updateConnectionUI('DISCONNECTED');
    state.ws = null;
  };

  ws.onerror = (err) => {
    log('err', 'WebSocket connection error');
    updateConnectionUI('ERROR');
  };
}

function sendJson(msg) {
  if (!state.ws || state.ws.readyState !== WebSocket.OPEN) return;
  const jsonStr = JSON.stringify(msg);
  log('tx', `[Control TX] ${jsonStr}`);
  state.ws.send(jsonStr);
}

function handleTextControlMessage(msg) {
  log('rx', `[Control RX] ${JSON.stringify(msg)}`);

  switch (msg.t) {
    case 'ready':
      log('success', `Handshake accepted! Negotiated codec: ${msg.codec}`);
      if (msg.settings) {
        state.settings.buttonA = msg.settings.button_a;
        state.settings.buttonB = msg.settings.button_b;
        state.settings.volume = msg.settings.volume;
        state.settings.captions = msg.settings.captions;
        state.settings.ledLevel = msg.settings.led_level;
        updateSettingsDisplay();
      }
      updateConnectionUI('IDLE');
      el.oledCaption.textContent = 'Ready. Hold [A] or [B] to Speak.';
      break;

    case 'turn.accepted':
      log('info', `Turn #${msg.turn_id} accepted (${msg.src} ➜ ${msg.dst})`);
      updateConnectionUI('LISTENING');
      el.oledTurnId.textContent = `TURN: #${msg.turn_id}`;
      el.oledCaption.textContent = `Listening (${msg.src.toUpperCase()})...`;
      startAudioUplink();
      break;

    case 'text':
      // Live transcript captions
      if (state.settings.captions) {
        el.oledCaption.textContent = `[${msg.kind}]: ${msg.delta}`;
      }
      break;

    case 'audio.start':
      log('info', `Downlink audio starting for turn #${msg.turn_id}`);
      state.stats.downlinkFrames = 0;
      el.statDownlinkFrames.textContent = '0';
      updateConnectionUI('PLAYING');
      el.oledCaption.textContent = 'Playing translated speech...';
      
      if (!state.stats.firstAudioMs && state.turnStartTime > 0) {
        state.stats.firstAudioMs = Date.now() - state.turnStartTime;
        el.statFirstAudio.textContent = `${state.stats.firstAudioMs} ms`;
      }
      break;

    case 'audio.end':
      log('info', `Downlink audio ended for turn #${msg.turn_id} (reason: ${msg.reason})`);
      if (state.turnStartTime > 0) {
        state.stats.totalLatencyMs = Date.now() - state.turnStartTime;
        el.statTotalLatency.textContent = `${state.stats.totalLatencyMs} ms`;
      }
      break;

    case 'error':
      log('err', `Server error [${msg.code}]: ${msg.message}`);
      el.oledCaption.textContent = `ERR: ${msg.message}`;
      updateConnectionUI('IDLE');
      break;

    case 'bye':
      log('warn', `Server sent bye: ${msg.reason}`);
      updateConnectionUI('DISCONNECTED');
      break;
  }
}

/* ==========================================================
   Binary Audio Framing & Audio Pipeline
   ========================================================== */

/**
 * Packs 8-byte big-endian header + raw PCM16 payload
 * [0] type (1=uplink, 2=downlink)
 * [1] flags (bit 0 = LAST_FRAME)
 * [2..3] turn_id (uint16 BE)
 * [4..7] seq (uint32 BE)
 * [8..] audio payload
 */
function packBinaryFrame(type, flags, turnId, seq, payload) {
  const buffer = new ArrayBuffer(8 + payload.byteLength);
  const view = new DataView(buffer);
  
  view.setUint8(0, type);
  view.setUint8(1, flags);
  view.setUint16(2, turnId, false); // Big endian
  view.setUint32(4, seq, false);    // Big endian
  
  new Uint8Array(buffer, 8).set(new Uint8Array(payload));
  return buffer;
}

function unpackBinaryHeader(arrayBuffer) {
  if (arrayBuffer.byteLength < 8) return null;
  const view = new DataView(arrayBuffer);
  return {
    type: view.getUint8(0),
    flags: view.getUint8(1),
    isLastFrame: (view.getUint8(1) & 0x01) !== 0,
    turnId: view.getUint16(2, false),
    seq: view.getUint32(4, false),
    payload: arrayBuffer.slice(8)
  };
}

// Downlink Audio Receiver
function handleBinaryAudioDownlink(arrayBuffer) {
  const frame = unpackBinaryHeader(arrayBuffer);
  if (!frame) return;

  state.stats.downlinkFrames++;
  el.statDownlinkFrames.textContent = state.stats.downlinkFrames;

  // Queue PCM16 payload for Web Audio loudspeaker playback
  queuePcmForPlayback(frame.payload);

  if (frame.isLastFrame) {
    log('info', `Received final downlink frame #${frame.seq} with LAST_FRAME flag.`);
  }
}

// Web Audio Loudspeaker Playback Queue
function queuePcmForPlayback(pcmArrayBuffer) {
  initAudioContext();
  if (!state.audioCtx) return;

  const int16View = new Int16Array(pcmArrayBuffer);
  const float32Samples = new Float32Array(int16View.length);
  for (let i = 0; i < int16View.length; i++) {
    float32Samples[i] = int16View[i] / 32768.0;
  }

  // Draw waveform on OLED
  drawWaveform(float32Samples);

  const audioBuf = state.audioCtx.createBuffer(1, float32Samples.length, 16000);
  audioBuf.getChannelData(0).set(float32Samples);

  const srcNode = state.audioCtx.createBufferSource();
  srcNode.buffer = audioBuf;

  const gainNode = state.audioCtx.createGain();
  gainNode.gain.value = (state.settings.volume / 100);
  srcNode.connect(gainNode);
  gainNode.connect(state.audioCtx.destination);

  const currentTime = state.audioCtx.currentTime;
  if (state.nextPlayTime < currentTime) {
    state.nextPlayTime = currentTime;
  }

  srcNode.start(state.nextPlayTime);
  state.nextPlayTime += audioBuf.duration;
  state.isPlayingDownlink = true;

  srcNode.onended = () => {
    if (state.audioCtx.currentTime >= state.nextPlayTime - 0.05) {
      // Finished all queued downlink audio
      state.isPlayingDownlink = false;
      log('info', 'Loudspeaker playback finished. Sending playback.done to server.');
      sendJson({
        t: 'playback.done',
        turn_id: state.currentTurnId
      });
      updateConnectionUI('IDLE');
      el.oledCaption.textContent = 'Finished. Hold [A] or [B] to Speak.';
      clearOledWaveform();
    }
  };
}

// Audio Context Init
function initAudioContext() {
  if (!state.audioCtx) {
    const AudioContext = window.AudioContext || window.webkitAudioContext;
    state.audioCtx = new AudioContext();
    el.audioCtxStatus.textContent = `Active (${state.audioCtx.sampleRate} Hz)`;
  }
  if (state.audioCtx.state === 'suspended') {
    state.audioCtx.resume();
  }
}

/* ==========================================================
   Microphone & Push-to-Talk Transmission
   ========================================================== */

let streamInterval = null;

function startTurn(button) {
  if (state.connectionState !== 'IDLE') {
    log('warn', `Cannot start turn in state: ${state.connectionState}`);
    return;
  }

  initAudioContext();
  state.activeButton = button;
  state.currentTurnId = (state.currentTurnId % 65535) + 1;
  state.uplinkSeq = 0;
  state.stats.uplinkFrames = 0;
  state.stats.firstAudioMs = null;
  state.stats.totalLatencyMs = null;
  state.turnStartTime = Date.now();
  state.nextPlayTime = 0;

  el.statUplinkFrames.textContent = '0';
  el.statDownlinkFrames.textContent = '0';
  el.statFirstAudio.textContent = '-- ms';
  el.statTotalLatency.textContent = '-- ms';

  updateConnectionUI('STARTING');
  el.oledTurnId.textContent = `TURN: #${state.currentTurnId}`;
  el.oledCaption.textContent = 'Requesting turn session...';

  // Send turn.start
  sendJson({
    t: 'turn.start',
    turn_id: state.currentTurnId,
    button: button
  });
}

function stopTurn() {
  if (state.connectionState !== 'LISTENING') return;

  log('info', `Button released. Sending turn.stop for turn #${state.currentTurnId}...`);
  updateConnectionUI('DRAINING');
  el.oledCaption.textContent = 'Translating speech...';

  // Stop audio uplink
  stopAudioUplink();

  // Send turn.stop
  sendJson({
    t: 'turn.stop',
    turn_id: state.currentTurnId
  });
}

function cancelTurn() {
  if (!state.currentTurnId) return;
  log('warn', `Aborting turn #${state.currentTurnId}...`);
  stopAudioUplink();
  sendJson({
    t: 'turn.cancel',
    turn_id: state.currentTurnId
  });
  updateConnectionUI('IDLE');
  el.oledCaption.textContent = 'Turn cancelled.';
  clearOledWaveform();
}

function startAudioUplink() {
  state.isRecording = true;

  if (el.radMic.checked && navigator.mediaDevices && navigator.mediaDevices.getUserMedia) {
    // Live Microphone capture
    navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, sampleRate: 16000 } })
      .then(stream => {
        state.micStream = stream;
        const source = state.audioCtx.createMediaStreamSource(stream);
        
        // Use ScriptProcessor / AudioWorklet to get 20ms chunks (320 samples = 640 bytes @ 16kHz)
        const bufferSize = 512;
        const processor = state.audioCtx.createScriptProcessor(bufferSize, 1, 1);
        source.connect(processor);
        processor.connect(state.audioCtx.destination);
        state.micProcessor = processor;

        let accumulated = [];
        processor.onaudioprocess = (e) => {
          if (!state.isRecording) return;
          const inputData = e.inputBuffer.getChannelData(0);
          drawWaveform(inputData);

          for (let i = 0; i < inputData.length; i++) {
            accumulated.push(inputData[i]);
            if (accumulated.length >= 320) { // 20ms @ 16kHz = 320 samples
              const chunk = accumulated.slice(0, 320);
              accumulated = accumulated.slice(320);
              sendAudioFrame(chunk);
            }
          }
        };
      })
      .catch(err => {
        log('warn', `Microphone permission denied: ${err.message}. Switching to Synthetic speech tone.`);
        el.radSynth.checked = true;
        startSyntheticAudioStream();
      });
  } else {
    startSyntheticAudioStream();
  }
}

function startSyntheticAudioStream() {
  // Streams 20 ms synthetic human speech frequency tone frames (640 bytes @ 16 kHz)
  let sampleCount = 0;
  streamInterval = setInterval(() => {
    if (!state.isRecording) return;
    const samples = new Float32Array(320);
    for (let i = 0; i < 320; i++) {
      const t = (sampleCount + i) / 16000;
      // Speech harmonic combination: 220Hz fundamental + 440Hz harmonic
      samples[i] = 0.4 * Math.sin(2 * Math.PI * 220 * t) + 0.2 * Math.sin(2 * Math.PI * 440 * t);
    }
    sampleCount += 320;
    drawWaveform(samples);
    sendAudioFrame(samples);
  }, 20);
}

function sendAudioFrame(float32Samples) {
  if (!state.ws || state.ws.readyState !== WebSocket.OPEN) return;

  // Convert Float32 [-1.0, 1.0] to Int16 Little-Endian (640 bytes)
  const pcm16 = new Int16Array(float32Samples.length);
  for (let i = 0; i < float32Samples.length; i++) {
    const s = Math.max(-1, Math.min(1, float32Samples[i]));
    pcm16[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
  }

  // Pack 8-byte big-endian binary frame (type: 1 = uplink)
  const frame = packBinaryFrame(1, 0, state.currentTurnId, state.uplinkSeq++, pcm16.buffer);
  state.ws.send(frame);

  state.stats.uplinkFrames++;
  el.statUplinkFrames.textContent = state.stats.uplinkFrames;
}

function stopAudioUplink() {
  state.isRecording = false;
  if (streamInterval) {
    clearInterval(streamInterval);
    streamInterval = null;
  }
  if (state.micProcessor) {
    state.micProcessor.disconnect();
    state.micProcessor = null;
  }
  if (state.micStream) {
    state.micStream.getTracks().forEach(t => t.stop());
    state.micStream = null;
  }
}

// OLED Waveform Visualizer
function drawWaveform(samples) {
  const w = el.oledCanvas.width;
  const h = el.oledCanvas.height;
  canvasCtx.fillStyle = '#030814';
  canvasCtx.fillRect(0, 0, w, h);

  canvasCtx.lineWidth = 1.5;
  canvasCtx.strokeStyle = '#38bdf8';
  canvasCtx.beginPath();

  const step = Math.ceil(samples.length / w);
  const mid = h / 2;
  for (let i = 0; i < w; i++) {
    const sample = samples[i * step] || 0;
    const y = mid + (sample * mid * 0.9);
    if (i === 0) canvasCtx.moveTo(i, y);
    else canvasCtx.lineTo(i, y);
  }
  canvasCtx.stroke();
}

function clearOledWaveform() {
  const w = el.oledCanvas.width;
  const h = el.oledCanvas.height;
  canvasCtx.fillStyle = '#030814';
  canvasCtx.fillRect(0, 0, w, h);
}

/* ==========================================================
   Event Listeners & User Interactions
   ========================================================== */

// Tab Navigation
document.querySelectorAll('.tab-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById(btn.dataset.tab).classList.add('active');
  });
});

// 1-Click Provision & Connect
el.btnOneClickSetup.addEventListener('click', async () => {
  el.btnOneClickSetup.disabled = true;
  el.btnOneClickSetup.textContent = '⏳ Provisioning...';
  try {
    const provisioned = await provisionDevice();
    if (provisioned) {
      const tokenReady = await fetchStreamToken();
      if (tokenReady) {
        connectWebSocket();
        await fetchSettings();
      }
    }
  } finally {
    el.btnOneClickSetup.disabled = false;
    el.btnOneClickSetup.textContent = '🚀 1-Click Provision & Connect';
  }
});

el.btnDisconnect.addEventListener('click', () => {
  if (state.ws) {
    state.ws.close();
    state.ws = null;
  }
  updateConnectionUI('DISCONNECTED');
});

el.btnClaimDevice.addEventListener('click', claimDevice);
el.btnSaveSettings.addEventListener('click', saveSettings);
el.btnRefreshSettings.addEventListener('click', fetchSettings);
el.btnQueryUsage.addEventListener('click', queryUsage);
el.btnCancelTurn.addEventListener('click', cancelTurn);
el.btnClearLogs.addEventListener('click', () => el.consoleLog.innerHTML = '');

el.btnPowerCycle.addEventListener('click', () => {
  log('info', 'Hardware power cycle triggered.');
  if (state.ws) state.ws.close();
  clearOledWaveform();
  setTimeout(() => {
    if (state.streamToken) connectWebSocket();
  }, 400);
});

// Speaker Test Tone
el.btnTestSpeaker.addEventListener('click', () => {
  log('info', 'Testing speaker output with 440Hz test tone...');
  const tone = new Float32Array(3200);
  for (let i = 0; i < 3200; i++) tone[i] = 0.3 * Math.sin(2 * Math.PI * 440 * (i / 16000));
  const pcm16 = new Int16Array(tone.length);
  for (let i = 0; i < tone.length; i++) pcm16[i] = tone[i] * 0x7FFF;
  queuePcmForPlayback(pcm16.buffer);
});

// Sliders Live Update
el.rangeVolume.addEventListener('input', (e) => {
  el.lblVolume.textContent = `${e.target.value}%`;
  el.oledVolume.textContent = `VOL: ${e.target.value}%`;
});
el.rangeLed.addEventListener('input', (e) => {
  el.lblLed.textContent = `${e.target.value}%`;
});

// Push-to-Talk Mouse / Touch Handlers
function setupPttEvents(buttonEl, buttonKey) {
  const pressStart = (e) => {
    e.preventDefault();
    buttonEl.classList.add('pressed');
    startTurn(buttonKey);
  };

  const pressEnd = (e) => {
    e.preventDefault();
    buttonEl.classList.remove('pressed');
    stopTurn();
  };

  buttonEl.addEventListener('mousedown', pressStart);
  buttonEl.addEventListener('mouseup', pressEnd);
  buttonEl.addEventListener('mouseleave', pressEnd);

  buttonEl.addEventListener('touchstart', pressStart, { passive: false });
  buttonEl.addEventListener('touchend', pressEnd, { passive: false });
}

setupPttEvents(el.btnA, 'a');
setupPttEvents(el.btnB, 'b');

// Keyboard Push-to-Talk (Hold Key A / Key B)
let keyAPressed = false;
let keyBPressed = false;

window.addEventListener('keydown', (e) => {
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;

  if (e.key === 'a' || e.key === 'A') {
    if (!keyAPressed) {
      keyAPressed = true;
      el.btnA.classList.add('pressed');
      startTurn('a');
    }
  } else if (e.key === 'b' || e.key === 'B') {
    if (!keyBPressed) {
      keyBPressed = true;
      el.btnB.classList.add('pressed');
      startTurn('b');
    }
  }
});

window.addEventListener('keyup', (e) => {
  if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;

  if (e.key === 'a' || e.key === 'A') {
    keyAPressed = false;
    el.btnA.classList.remove('pressed');
    stopTurn();
  } else if (e.key === 'b' || e.key === 'B') {
    keyBPressed = false;
    el.btnB.classList.remove('pressed');
    stopTurn();
  }
});

// Auto-detect current window origin if served from server
if (window.location.origin && window.location.origin.startsWith('http')) {
  el.serverUrl.value = window.location.origin;
}

// Initial draw
clearOledWaveform();
log('info', 'Parlex ESP32-S3 Demo client initialized.');
