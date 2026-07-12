# VitalLink — Technical Architecture Map

## 1. System Overview

Three entities, two distinct communication links:

```
┌──────────────────┐         UDP (degraded link)         ┌──────────────┐        WebSocket/HTTP        ┌──────────────────┐
│  FIELD HEALTH     │ ◄──────────────────────────────────► │    SERVER    │ ◄───────────────────────────► │  DOCTOR          │
│  WORKER (Client)  │   custom protocol, 20%+ loss,        │  (Gateway +   │   normal clinic connectivity  │  DASHBOARD        │
│                    │   <64kbps, low-power binary          │   EHR store) │                                │  (Specialist)     │
└──────────────────┘                                       └──────────────┘                                └──────────────────┘
```

**Key design principle:** the resilience engineering (custom UDP protocol, ARQ, delta-encoding) is scoped to the **Field Worker ↔ Server** link only — that's where the hackathon's "extreme network conditions" constraint actually lives. The **Server ↔ Doctor** link runs on standard clinic infrastructure and uses conventional web protocols, since over-engineering that side adds no value and burns build time.

### Tech Stack

| Component                          | Language/Tech                                                                            | Why                                                                                                                                                                                                                       |
| ---------------------------------- | ---------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Field Client & Server**          | Go                                                                                       | Compiles to a single static binary (no runtime deps), cross-compiles trivially to ARM for low-power SBCs, native low-level UDP socket control. Sharing Go for both client and server allows shared protocol code.         |
| **Mobile App**                     | Android / Kotlin                                                                         | High-performance native Android application with local validation, background services, and ruggedized UI tailored for clinical fieldwork.                                                                                |
| **Doctor Dashboard**               | HTML + vanilla JS + CSS                                                                  | Native `WebSocket` API and Chart.js via CDN for live vitals graphs. Lightweight, no build step, instantly loadable over basic connections.                                                                                |
| **EHR Storage (Local Demo)**       | SQLite via `modernc.org/sqlite`                                                          | Zero external DB setup, file-based pure-Go driver to avoid cgo dependencies.                                                                                                                                              |
| **Network Degradation Sim**        | Linux `tc`/`netem` (shell scripts)                                                       | Standard, reliable loss/bandwidth injection to validate the transport layer under strict conditions.                                                                                                                        |

---

## 2. Entity Descriptions

| Entity | Role | Runs On | Responsibilities |
|---|---|---|---|
| **Field Health Worker (Client)** | Captures and transmits patient data from the field | Lightweight binary — low-power SBC or basic mobile device | Initiates session, streams vitals, receives doctor acknowledgments/messages |
| **Server** | Relay + session broker + EHR store | Standard server (can be local for demo) | Routes packets, manages session state, looks up EHR records, bridges the two links |
| **Doctor Dashboard** | Specialist's receiving interface | Standard web app, normal connectivity | Displays EHR context, displays live vitals feed, sends messages/instructions back |

---

## 3. Communication Channels

| Link | Protocol | Why |
|---|---|---|
| Field Worker ↔ Server | Raw UDP + custom application-layer reliability scheme (selective-repeat ARQ) | TCP's built-in congestion control stalls under high loss; UDP gives full control over retransmission tuned for small, latency-sensitive payloads |
| Server ↔ Doctor | WebSocket (preferred) or HTTP polling | No constrained-network requirement here — standard reliability is fine, don't over-engineer |

---

## 4. Initial Communication (Session Handshake)

**Purpose:** establish a session, authenticate the patient context, and get the doctor ready — happens **once** per session, before any vitals flow.

### Sequence

1. `SESSION_INIT` — Field Worker → Server
2. `SESSION_ACK` — Server → Field Worker (fast, confirms session exists)
3. `EHR_PUSH` — Server → Doctor (parallel, once patient_id resolves)
4. `DOCTOR_READY` — Doctor → Server → Field Worker (once doctor's dashboard has loaded EHR and is watching)

### Packet Specs

**`SESSION_INIT`** (Field Worker → Server, UDP)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x01` = SESSION_INIT |
| `worker_id` | 4B | Identifies the field device/worker |
| `patient_id` | 4B | Patient lookup key for EHR |
| `timestamp` | 4B | Unix timestamp of request |
| `checksum` | 2B | Integrity check |

**`SESSION_ACK`** (Server → Field Worker, UDP)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x02` = SESSION_ACK |
| `session_token` | 4B | Unique session identifier, used in all subsequent packets |
| `status_code` | 1B | `0x00` ok / `0x01` patient not found / `0x02` no doctor available |
| `server_time` | 4B | For clock sync between client and server |
| `checksum` | 2B | Integrity check |

**`EHR_PUSH`** (Server → Doctor, WebSocket, JSON — no size constraint here since it's the unconstrained link)

| Field | Type | Description |
|---|---|---|
| `session_token` | string | Ties this EHR to the incoming session |
| `patient_id` | string | Patient identifier |
| `demographics` | object | Name, age, sex |
| `known_conditions` | array | Pre-existing conditions |
| `allergies` | array | Known allergies |
| `medications` | array | Current medications |
| `last_visit_notes` | string | Most recent clinical notes |
| `worker_id` | string | Which field worker/device initiated this session |

**`DOCTOR_READY`** (Doctor → Server → Field Worker; WebSocket then relayed as UDP)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x03` = DOCTOR_READY |
| `session_token` | 4B | Matches active session |
| `doctor_id` | 2B | Identifies responding specialist |
| `message` | short string, fixed max length (e.g. 32B) | Human-readable status, e.g. "Doctor connected" |

---

## 5. Continuous Communication (Steady-State Vitals Streaming)

**Purpose:** ongoing transmission of live patient vitals once a session is active — repeats every N seconds until session ends.

### Sequence (repeating loop)

1. `VITALS` — Field Worker → Server → Doctor
2. `VITALS_ACK` — Server → Field Worker
3. (optional, as needed) `DOCTOR_MSG` — Doctor → Server → Field Worker
4. `HEARTBEAT` — either direction, low frequency, confirms link is alive between vitals sends
5. `SESSION_END` — Field Worker → Server, terminates session

### Packet Specs

**`VITALS`** (Field Worker → Server, UDP, repeats)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x10` = VITALS |
| `session_token` | 4B | Ties packet to active session |
| `seq_num` | 2B | For ordering/dedup/ACK tracking |
| `heart_rate` | 1B | BPM |
| `spo2` | 1B | Blood oxygen % |
| `bp_systolic` | 1B | mmHg |
| `bp_diastolic` | 1B | mmHg |
| `temp` | 1B | Body temp (scaled int, e.g. x10 for one decimal) |
| `delta_flag` | 1B | `1` = only changed fields sent since last packet, `0` = full snapshot |
| `timestamp` | 4B | Capture time |
| `checksum` | 2B | Integrity check |

**`VITALS_ACK`** (Server → Field Worker, UDP)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x11` = VITALS_ACK |
| `session_token` | 4B | Matches session |
| `ack_seq_num` | 2B | Confirms which packet was received |

**`DOCTOR_MSG`** (Doctor → Server → Field Worker; optional, lightweight)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x12` = DOCTOR_MSG |
| `session_token` | 4B | Matches session |
| `code` | 1B | Pre-defined instruction code (e.g. `0x01` = "stand by", `0x02` = "administer O2") — coded, not free text, to stay lightweight over the constrained return path |

**`HEARTBEAT`** (either direction, UDP, minimal)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x20` = HEARTBEAT |
| `session_token` | 4B | Matches session |

**`SESSION_END`** (Field Worker → Server, UDP)

| Field | Type/Size | Description |
|---|---|---|
| `packet_type` | 1B | `0x30` = SESSION_END |
| `session_token` | 4B | Matches session |

---

## 6. Initial vs. Continuous Communication — Key Differences

| Aspect | Initial (Handshake) | Continuous (Vitals Stream) |
|---|---|---|
| **Frequency** | Once per session | Repeats every N seconds for session duration |
| **Payload size** | Larger where it matters (EHR push is the heaviest single payload — but travels over the *unconstrained* Server↔Doctor link, so size isn't a problem) | Small, fixed-width binary packets — every byte matters on the constrained link |
| **Reliability model** | Must eventually succeed — can afford longer retry timeouts since it only happens once and nothing downstream can proceed without it | Freshness-over-completeness — a vitals packet from 10 seconds ago is less valuable than the current one, so retransmission timeouts are short and very old un-ACKed packets may be dropped rather than endlessly retried |
| **Ordering requirement** | Strict — session must exist before vitals can be tied to it | Loosely ordered — sequence numbers used mainly for dedup and ACK tracking, not strict in-order delivery |
| **What "success" means** | Session exists, doctor has context (EHR), doctor is confirmed present | Doctor's dashboard reflects patient's *current* state within an acceptable latency window, even if some individual packets are lost |
| **Failure handling** | Explicit status codes (patient not found, no doctor available) — field worker needs to know clearly if setup failed | Graceful degradation — dashboard shows "last known vitals as of Xs ago" rather than failing outright on a single dropped packet |
| **Link used** | Both links (UDP for client-server, WebSocket for server-doctor) | Primarily UDP client-server; server-doctor forwarding is simple pass-through over WebSocket |

---

## 7. Design Notes for the Demo

- **Show both phases live:** the handshake proving EHR retrieval works, then the continuous stream proving vitals survive under `netem`-simulated 20%+ loss and <64kbps.
- **The freshness-over-completeness principle for VITALS is a strong technical talking point** — it shows you understood that medical vitals streaming has different reliability requirements than a one-time session setup, rather than applying the same retry logic everywhere.
- **Coded `DOCTOR_MSG` instead of free text** keeps the return path lightweight and gives you a clean answer if asked "what about doctor-to-field communication under bad conditions" — you've already accounted for it, minimally.
