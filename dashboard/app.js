/* =========================================================
   VitalLink Doctor Dashboard — app.js
   Handles WebSocket connection, UI updates, and Chart.js
   ========================================================= */

// State
let ws = null;
let currentSessionToken = null;
let lastVitalsTime = null;
let staleCheckInterval = null;
let lastSeqNum = -1;
let pktsRx = 0;
let estLoss = 0;

// Chart instance
let vitalsChart = null;

// DOM Elements
const els = {
  overlay: document.getElementById('ws-overlay'),
  wsUrl: document.getElementById('ui-ws-url'),
  
  session: document.getElementById('ui-session'),
  worker: document.getElementById('ui-worker'),
  statusBadge: document.getElementById('ui-status-badge'),
  statusText: document.getElementById('ui-status-text'),
  btnReady: document.getElementById('btn-ready'),
  
  ehrContent: document.getElementById('ehr-content'),
  
  hr: { val: document.getElementById('val-hr'), stat: document.getElementById('stat-hr'), card: document.getElementById('card-hr') },
  spo2: { val: document.getElementById('val-spo2'), stat: document.getElementById('stat-spo2'), card: document.getElementById('card-spo2') },
  bp: { 
    sys: document.getElementById('val-bps'), 
    dia: document.getElementById('val-bpd'), 
    stat: document.getElementById('stat-bp'), 
    card: document.getElementById('card-bp') 
  },
  temp: { val: document.getElementById('val-temp'), stat: document.getElementById('stat-temp'), card: document.getElementById('card-temp') },
  
  staleNotice: document.getElementById('stale-notice'),
  staleSec: document.getElementById('val-stale-sec'),
  
  imgStage: document.getElementById('img-stage'),
  imgPlaceholder: document.getElementById('img-placeholder'),
  imgRender: document.getElementById('img-render'),
  imgProgress: document.getElementById('img-progress'),
  imgProgressFill: document.getElementById('img-progress-fill'),
  imgChunkN: document.getElementById('img-chunk-n'),
  imgChunkTotal: document.getElementById('img-chunk-total'),
  imgChunkGrid: document.getElementById('img-chunk-grid'),
  
  audioLog: document.getElementById('audio-log'),
  audioEmpty: document.getElementById('audio-empty'),
  
  msgCodes: document.querySelectorAll('.msg-code-btn'),
  
  statRx: document.getElementById('stat-rx'),
  statLoss: document.getElementById('stat-loss'),
  statLag: document.getElementById('stat-lag')
};

// Initialize
function init() {
  els.wsUrl.textContent = CONFIG.WS_URL;
  initChart();
  connectWS();
  
  els.btnReady.addEventListener('click', sendDoctorReady);
  els.msgCodes.forEach(btn => {
    btn.addEventListener('click', () => sendDoctorMsg(btn));
  });
  
  // Stale check loop
  setInterval(checkStaleVitals, 1000);
}

// WebSocket Connection
function connectWS() {
  els.overlay.classList.add('visible');
  
  ws = new WebSocket(CONFIG.WS_URL);
  
  ws.onopen = () => {
    els.overlay.classList.remove('visible');
    console.log('WS Connected');
    setGlobalStatus('idle');
  };
  
  ws.onclose = () => {
    els.overlay.classList.add('visible');
    setGlobalStatus('disconnected');
    setTimeout(connectWS, 3000);
  };
  
  ws.onerror = (err) => {
    console.error('WS Error:', err);
  };
  
  ws.onmessage = (event) => {
    try {
      const msg = JSON.parse(event.data);
      handleMessage(msg);
    } catch (e) {
      console.error('Failed to parse WS message:', e);
    }
  };
}

// Message Router
function handleMessage(msg) {
  switch (msg.type) {
    case 'session_status':
      handleSessionStatus(msg);
      break;
    case 'ehr_push':
      handleEHRPush(msg);
      break;
    case 'vitals':
      handleVitals(msg);
      break;
    case 'media':
      handleMedia(msg);
      break;
  }
}

// Handlers
function handleSessionStatus(msg) {
  currentSessionToken = msg.session_token;
  els.session.textContent = msg.session_token;
  
  if (msg.status === 'active') {
    setGlobalStatus('active');
    els.btnReady.disabled = false;
    els.msgCodes.forEach(btn => btn.disabled = false);
    resetStats();
  } else if (msg.status === 'ended') {
    setGlobalStatus('ended');
    els.btnReady.disabled = true;
    els.msgCodes.forEach(btn => btn.disabled = true);
  }
}

function handleEHRPush(msg) {
  els.worker.textContent = msg.worker_id;
  
  // Build tags HTML
  const conditionTags = msg.known_conditions.map(c => `<span class="ehr-tag ehr-tag--condition">${c}</span>`).join('');
  const allergyTags = msg.allergies.map(a => `<span class="ehr-tag ehr-tag--allergy">${a}</span>`).join('');
  const medTags = msg.medications.map(m => `<span class="ehr-tag ehr-tag--med">${m}</span>`).join('');
  
  const html = `
    <div class="ehr__name">${msg.demographics.name}</div>
    <div class="ehr__sub">${msg.demographics.age}y • ${msg.demographics.sex} • ID: ${msg.patient_id}</div>
    
    <div class="ehr-section">
      <div class="ehr-section__label">Known Conditions</div>
      <div class="ehr-tags">${conditionTags || '<span class="ehr-section__text">None</span>'}</div>
    </div>
    
    <div class="ehr-section">
      <div class="ehr-section__label">Allergies</div>
      <div class="ehr-tags">${allergyTags || '<span class="ehr-section__text">No known allergies</span>'}</div>
    </div>
    
    <div class="ehr-section">
      <div class="ehr-section__label">Current Medications</div>
      <div class="ehr-tags">${medTags || '<span class="ehr-section__text">None</span>'}</div>
    </div>
    
    <div class="ehr-section">
      <div class="ehr-section__label">Last Visit Notes</div>
      <div class="ehr__notes">${msg.last_visit_notes || 'No recent notes.'}</div>
    </div>
  `;
  
  els.ehrContent.innerHTML = html;
}

function handleVitals(msg) {
  lastVitalsTime = Date.now();
  els.staleNotice.classList.remove('visible');
  
  // Calculate network stats
  pktsRx++;
  if (lastSeqNum !== -1) {
    const gap = msg.seq_num - lastSeqNum;
    if (gap > 1) {
      estLoss += (gap - 1);
    }
  }
  lastSeqNum = msg.seq_num;
  
  // Calculate lag based on timestamp (if client and server clocks are relatively close, or just track time since last rx)
  // For demo, we'll just show the time since the packet was generated vs now (assuming roughly synced, or simulated)
  const lagMs = Date.now() - (msg.timestamp * 1000); // msg.timestamp is likely unix epoch if generated properly
  
  els.statRx.textContent = pktsRx;
  els.statLoss.textContent = estLoss;
  // Make sure lag isn't wildly negative due to clock skew, cap at 0 for display
  els.statLag.textContent = `${Math.max(0, lagMs)}ms`;
  
  // Update UI values
  updateVitalCard(els.hr, msg.heart_rate, getHRState(msg.heart_rate));
  updateVitalCard(els.spo2, msg.spo2, getSpO2State(msg.spo2));
  updateVitalCard({val: els.bp.sys, stat: els.bp.stat, card: els.bp.card}, msg.bp_systolic, getBPState(msg.bp_systolic, msg.bp_diastolic));
  els.bp.dia.textContent = msg.bp_diastolic;
  updateVitalCard(els.temp, msg.temp_c.toFixed(1), getTempState(msg.temp_c));
  
  // Update Chart
  const timeLabel = new Date().toLocaleTimeString([], { hour12: false, hour: '2-digit', minute:'2-digit', second:'2-digit' });
  addDataToChart(timeLabel, msg.heart_rate, msg.spo2);
}

function handleMedia(msg) {
  if (msg.kind === 'image') {
    // For demo purposes, we simulate the progressive loading that *would* happen 
    // chunk-by-chunk on the server, since the server only sends us the final reassembled image.
    simulateProgressiveImage(msg.data_base64);
  } else if (msg.kind === 'audio') {
    addAudioEntry(msg.data_base64);
  }
}

// UI State Helpers
function setGlobalStatus(status) {
  els.statusBadge.className = `badge badge--${status}`;
  
  const texts = {
    'active': 'Session Active',
    'idle': 'Waiting for Field',
    'ended': 'Session Ended',
    'disconnected': 'Gateway Disconnected'
  };
  els.statusText.textContent = texts[status];
}

function updateVitalCard(uiElements, value, stateObj) {
  uiElements.val.textContent = value;
  uiElements.stat.textContent = stateObj.text;
  
  uiElements.card.className = 'vital-card';
  if (uiElements.card.id === 'card-bp') uiElements.card.classList.add('vital-card--bp');
  uiElements.card.classList.add(`vital-card--${stateObj.level}`);
}

function resetStats() {
  pktsRx = 0;
  estLoss = 0;
  lastSeqNum = -1;
  els.statRx.textContent = '0';
  els.statLoss.textContent = '0';
  els.statLag.textContent = '0ms';
  
  if (vitalsChart) {
    vitalsChart.data.labels = [];
    vitalsChart.data.datasets[0].data = [];
    vitalsChart.data.datasets[1].data = [];
    vitalsChart.update();
  }
}

function checkStaleVitals() {
  if (!lastVitalsTime || els.statusBadge.classList.contains('badge--ended')) return;
  
  const elapsed = Date.now() - lastVitalsTime;
  if (elapsed > CONFIG.STALE_THRESHOLD_MS) {
    els.staleNotice.classList.add('visible');
    els.staleSec.textContent = Math.floor(elapsed / 1000);
  } else {
    els.staleNotice.classList.remove('visible');
  }
}

// Clinical Reference Ranges
function getHRState(hr) {
  if (hr < 50 || hr > 120) return { level: 'critical', text: 'Critical' };
  if (hr < 60 || hr > 100) return { level: 'warning', text: 'Abnormal' };
  return { level: 'stable', text: 'Normal' };
}
function getSpO2State(spo2) {
  if (spo2 < 90) return { level: 'critical', text: 'Critical Hypoxia' };
  if (spo2 < 95) return { level: 'warning', text: 'Low' };
  return { level: 'stable', text: 'Normal' };
}
function getBPState(sys, dia) {
  if (sys < 90 || sys > 180 || dia > 120) return { level: 'critical', text: 'Critical' };
  if (sys > 130 || dia > 80) return { level: 'warning', text: 'Elevated' };
  return { level: 'stable', text: 'Normal' };
}
function getTempState(temp) {
  if (temp < 35.0 || temp > 39.5) return { level: 'critical', text: 'Critical' };
  if (temp < 36.0 || temp > 37.8) return { level: 'warning', text: 'Abnormal' };
  return { level: 'stable', text: 'Normal' };
}

// Outgoing Messages
function sendDoctorReady() {
  if (!ws || ws.readyState !== WebSocket.OPEN || !currentSessionToken) return;
  
  const payload = {
    type: 'doctor_ready',
    session_token: currentSessionToken.toString(),
    doctor_id: CONFIG.DOCTOR_ID,
    message: 'Specialist watching'
  };
  ws.send(JSON.stringify(payload));
  
  els.btnReady.textContent = 'Ready Sent';
  els.btnReady.classList.replace('btn--primary', 'btn--outline');
  setTimeout(() => {
    els.btnReady.textContent = 'Signal Doctor Ready';
    els.btnReady.classList.replace('btn--outline', 'btn--primary');
  }, 3000);
}

function sendDoctorMsg(btn) {
  if (!ws || ws.readyState !== WebSocket.OPEN || !currentSessionToken) return;
  
  const code = parseInt(btn.dataset.code, 10);
  const payload = {
    type: 'doctor_msg',
    session_token: currentSessionToken.toString(),
    code: code
  };
  ws.send(JSON.stringify(payload));
  
  // Visual feedback
  btn.classList.add('msg-code-btn--sent');
  setTimeout(() => {
    btn.classList.remove('msg-code-btn--sent');
  }, 2000);
}

// Chart.js Setup
function initChart() {
  const ctx = document.getElementById('vitalsChart').getContext('2d');
  
  vitalsChart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: [],
      datasets: [
        {
          label: 'HR (bpm)',
          borderColor: '#1B6CA8', // signal
          backgroundColor: 'rgba(27, 108, 168, 0.1)',
          borderWidth: 2,
          pointRadius: 0,
          tension: 0.3,
          data: [],
          yAxisID: 'y'
        },
        {
          label: 'SpO2 (%)',
          borderColor: '#1E7E5A', // stable
          borderWidth: 2,
          pointRadius: 0,
          borderDash: [5, 5],
          tension: 0.3,
          data: [],
          yAxisID: 'y1'
        }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: true, position: 'top', labels: { usePointStyle: true, boxWidth: 6, font: { family: "'DM Sans', sans-serif", size: 11 } } },
        tooltip: { enabled: true }
      },
      scales: {
        x: {
          display: true,
          grid: { display: false },
          ticks: { font: { family: "'DM Mono', monospace", size: 10 }, maxTicksLimit: 6 }
        },
        y: {
          type: 'linear',
          display: true,
          position: 'left',
          title: { display: false },
          grid: { color: '#E2E6EA' },
          min: 40, max: 180,
          ticks: { font: { family: "'DM Mono', monospace", size: 10 } }
        },
        y1: {
          type: 'linear',
          display: true,
          position: 'right',
          title: { display: false },
          grid: { display: false },
          min: 80, max: 100,
          ticks: { font: { family: "'DM Mono', monospace", size: 10 } }
        }
      }
    }
  });
}

function addDataToChart(label, hr, spo2) {
  vitalsChart.data.labels.push(label);
  vitalsChart.data.datasets[0].data.push(hr);
  vitalsChart.data.datasets[1].data.push(spo2);
  
  if (vitalsChart.data.labels.length > CONFIG.VITALS_CHART_WINDOW) {
    vitalsChart.data.labels.shift();
    vitalsChart.data.datasets[0].data.shift();
    vitalsChart.data.datasets[1].data.shift();
  }
  
  vitalsChart.update('none'); // disable animation for live feed
}

// Media: Progressive Image Simulation (since server sends whole base64)
function simulateProgressiveImage(base64Data) {
  els.imgPlaceholder.classList.add('hidden');
  els.imgRender.classList.add('hidden');
  els.imgProgress.classList.add('visible');
  
  const totalChunks = Math.floor(Math.random() * 10) + 15; // 15-25 chunks
  els.imgChunkTotal.textContent = totalChunks;
  els.imgChunkN.textContent = '0';
  
  // Setup grid
  els.imgChunkGrid.innerHTML = '';
  for (let i = 0; i < totalChunks; i++) {
    const cell = document.createElement('div');
    cell.className = 'chunk-cell';
    els.imgChunkGrid.appendChild(cell);
  }
  
  let currentChunk = 0;
  
  const interval = setInterval(() => {
    currentChunk++;
    els.imgChunkN.textContent = currentChunk;
    els.imgProgressFill.style.width = `${(currentChunk / totalChunks) * 100}%`;
    
    if (els.imgChunkGrid.children[currentChunk - 1]) {
      els.imgChunkGrid.children[currentChunk - 1].classList.add('chunk-cell--received');
    }
    
    // Simulate a NACK/delay occasionally
    if (Math.random() > 0.8 && currentChunk < totalChunks) {
      if (els.imgChunkGrid.children[currentChunk]) {
        els.imgChunkGrid.children[currentChunk].classList.add('chunk-cell--missing');
      }
    }
    
    if (currentChunk >= totalChunks) {
      clearInterval(interval);
      setTimeout(() => {
        els.imgProgress.classList.remove('visible');
        els.imgRender.src = `data:image/jpeg;base64,${base64Data}`;
        els.imgRender.classList.remove('hidden');
      }, 500);
    }
  }, 150); // fast simulation
}

// Media: Audio Log
function addAudioEntry(base64Data) {
  if (els.audioEmpty) {
    els.audioEmpty.remove();
    els.audioEmpty = null;
  }
  
  const timeStr = new Date().toLocaleTimeString([], { hour12: false });
  
  const entry = document.createElement('div');
  entry.className = 'audio-entry';
  entry.innerHTML = `
    <div class="audio-entry__meta">Audio Clip Rx • ${timeStr}</div>
    <audio class="audio-entry__player" controls src="data:audio/ogg;base64,${base64Data}"></audio>
  `;
  
  els.audioLog.prepend(entry);
  
  // Keep only last 5
  if (els.audioLog.children.length > 5) {
    els.audioLog.lastChild.remove();
  }
}

// Start
document.addEventListener('DOMContentLoaded', init);
