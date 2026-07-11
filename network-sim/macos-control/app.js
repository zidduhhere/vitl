// VitalLink macOS Network Conditions Control Panel

const lossRange = document.getElementById("lossRange");
const lossNumber = document.getElementById("lossNumber");
const rateRange = document.getElementById("rateRange");
const rateNumber = document.getElementById("rateNumber");
const ifaceInput = document.getElementById("iface");
const applyBtn = document.getElementById("applyBtn");
const resetBtn = document.getElementById("resetBtn");
const statusEl = document.getElementById("status");

function syncPair(rangeEl, numberEl) {
  rangeEl.addEventListener("input", () => {
    numberEl.value = rangeEl.value;
  });
  numberEl.addEventListener("input", () => {
    rangeEl.value = numberEl.value;
  });
}

syncPair(lossRange, lossNumber);
syncPair(rateRange, rateNumber);

function setStatus(message, kind) {
  statusEl.textContent = message;
  statusEl.className = "status" + (kind ? " " + kind : "");
}

async function postJSON(path, body) {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok || !data.ok) {
    throw new Error(data.error || `request to ${path} failed`);
  }
  return data;
}

applyBtn.addEventListener("click", async () => {
  applyBtn.disabled = true;
  setStatus("Applying…", "");
  try {
    await postJSON("/apply", {
      interface: ifaceInput.value.trim(),
      loss_pct: Number(lossNumber.value),
      rate_mbit: Number(rateNumber.value),
    });
    setStatus(
      `Applied: ${lossNumber.value}% loss, ${rateNumber.value} Mbit/s on ${ifaceInput.value.trim()}`,
      "ok"
    );
  } catch (err) {
    setStatus(`Error: ${err.message}`, "error");
  } finally {
    applyBtn.disabled = false;
  }
});

resetBtn.addEventListener("click", async () => {
  resetBtn.disabled = true;
  setStatus("Resetting…", "");
  try {
    await postJSON("/reset");
    setStatus("Reset complete.", "ok");
  } catch (err) {
    setStatus(`Error: ${err.message}`, "error");
  } finally {
    resetBtn.disabled = false;
  }
});
