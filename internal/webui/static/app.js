/* Copyright (C) 2026 Joern Ihlenburg
   SPDX-License-Identifier: GPL-3.0-or-later */

"use strict";

/* The session token arrives once in the URL; keep it in memory only and
   strip it from the address bar and browser history immediately. */
const token = new URLSearchParams(location.search).get("token") || "";
if (location.search) {
  history.replaceState(null, "", location.pathname);
}

const $ = (id) => document.getElementById(id);

let selectedRecord = null;
let revokeToken = "";

function showMessage(text, isError) {
  const message = $("message");
  message.textContent = text;
  message.classList.toggle("error", Boolean(isError));
  message.hidden = false;
}

async function api(path, options) {
  const response = await fetch(path, {
    ...options,
    headers: {
      "X-BOM-Builder-Token": token,
      ...(options && options.body ? { "Content-Type": "application/json" } : {}),
    },
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok || payload.status === "error") {
    throw new Error(payload.message || `request failed (${response.status})`);
  }
  return payload;
}

function formatTime(value) {
  return value ? value.replace("T", " ").replace(/\.\d+Z$/, "Z") : "";
}

async function refresh() {
  const status = await api("/api/status");
  const info = status.resolutions;
  $("store-line").textContent =
    `${info.path} — active ${info.active_count}, superseded ${info.superseded_count}, ` +
    `revoked ${info.revoked_count}, events ${info.event_count}`;
  $("resolver-card").hidden = !status.resolver;

  const include = $("include-inactive").checked;
  const list = await api(`/api/resolutions?include_inactive=${include}`);
  renderRecords(list.records || []);
}

function renderRecords(records) {
  const body = $("records-body");
  body.replaceChildren();
  for (const record of records) {
    const row = document.createElement("tr");
    row.classList.toggle("inactive", record.status !== "active");
    row.classList.toggle("revoked", record.status === "revoked");
    row.classList.toggle(
      "selected",
      Boolean(selectedRecord) && record.resolution_id === selectedRecord.resolution_id,
    );
    appendCell(row, record.status, "badge " + (record.status === "active" ? "active" : ""));
    appendCell(row, `${record.manufacturer} ${record.part_number}`);
    appendCell(row, `${record.replacement.manufacturer} ${record.replacement.part_number}`);
    appendCell(
      row,
      record.replacement.provider
        ? `${record.replacement.provider} ${record.replacement.provider_sku || ""}`
        : "",
    );
    appendCell(row, record.approved_by);
    appendCell(row, formatTime(record.updated_at));
    row.addEventListener("click", () => showDetail(record));
    body.appendChild(row);
  }
}

function appendCell(row, text, badgeClass) {
  const cell = document.createElement("td");
  if (badgeClass) {
    const badge = document.createElement("span");
    badge.className = badgeClass;
    badge.textContent = text;
    cell.appendChild(badge);
  } else {
    cell.textContent = text;
  }
  row.appendChild(cell);
}

async function showDetail(record) {
  selectedRecord = record;
  revokeToken = "";
  $("detail-card").hidden = false;
  $("detail-title").textContent = `Resolution ${record.resolution_id}`;
  const detail = $("detail-list");
  detail.replaceChildren();
  const rows = [
    ["Status", record.status],
    ["Original", `${record.manufacturer} ${record.part_number}`],
    ["Replacement", `${record.replacement.manufacturer} ${record.replacement.part_number}`],
  ];
  if (record.replacement.provider) {
    rows.push(["Provider", `${record.replacement.provider} ${record.replacement.provider_sku || ""}`]);
  }
  rows.push(
    ["Approved by", record.approved_by],
    ["Approved at", formatTime(record.approved_at)],
    ["Updated at", formatTime(record.updated_at)],
  );
  if (record.note) rows.push(["Note", record.note]);
  for (const document_ of record.source_documents || []) {
    rows.push(["Evidence", `${document_.url} (sha256:${document_.sha256})`]);
  }
  for (const [term, value] of rows) {
    const dt = document.createElement("dt");
    dt.textContent = term;
    const dd = document.createElement("dd");
    dd.textContent = value;
    detail.append(dt, dd);
  }

  $("revoke-area").hidden = record.status !== "active";
  $("revoke-preview").hidden = true;
  $("revoke-button").textContent = "Preview revoke";
  $("revoke-form").reset();

  const history = await api(
    `/api/history?manufacturer=${encodeURIComponent(record.manufacturer)}` +
      `&part=${encodeURIComponent(record.part_number)}`,
  );
  const list = $("history-list");
  list.replaceChildren();
  for (const event of history.events || []) {
    const item = document.createElement("li");
    item.textContent =
      `${formatTime(event.occurred_at)} — ${event.action} ${event.resolution_id}` +
      ` by ${event.actor}` +
      (event.details ? ` (${event.details})` : "");
    list.appendChild(item);
  }
  refresh().catch((error) => showMessage(error.message, true));
}

$("approve-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(event.target);
  const request = {
    manufacturer: form.get("manufacturer"),
    part_number: form.get("part_number"),
    replacement: {
      manufacturer: form.get("replacement_manufacturer"),
      part_number: form.get("replacement_part_number"),
    },
    approved_by: form.get("approved_by"),
  };
  if (form.get("provider")) request.replacement.provider = form.get("provider");
  if (form.get("provider_sku")) request.replacement.provider_sku = form.get("provider_sku");
  if (form.get("note")) request.note = form.get("note");
  try {
    const result = await api("/api/approve", {
      method: "POST",
      body: JSON.stringify(request),
    });
    event.target.reset();
    await refresh();
    /* The message is the completion signal: it appears only after the
       table already reflects the new state. */
    let text = `approved resolution ${result.resolution.resolution_id}`;
    if (result.superseded) text += ` (superseded ${result.superseded.resolution_id})`;
    showMessage(text, false);
  } catch (error) {
    showMessage(error.message, true);
  }
});

$("revoke-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!selectedRecord) return;
  const form = new FormData(event.target);
  const request = {
    resolution_id: selectedRecord.resolution_id,
    revoked_by: form.get("revoked_by"),
    reason: form.get("reason") || "",
  };
  try {
    if (!revokeToken) {
      /* First submit: a read-only preview bound to the record content. */
      const preview = await api("/api/revoke", {
        method: "POST",
        body: JSON.stringify(request),
      });
      revokeToken = preview.revoke.apply_token;
      $("revoke-preview").textContent =
        "Preview only — nothing changed yet. Submit again to revoke " +
        `${preview.revoke.record.manufacturer} ${preview.revoke.record.part_number} ` +
        `-> ${preview.revoke.record.replacement.part_number}.`;
      $("revoke-preview").hidden = false;
      $("revoke-button").textContent = "Confirm revoke";
      $("revoke-button").classList.add("danger");
      return;
    }
    request.apply_token = revokeToken;
    const applied = await api("/api/revoke", {
      method: "POST",
      body: JSON.stringify(request),
    });
    revokeToken = "";
    $("revoke-button").classList.remove("danger");
    $("detail-card").hidden = true;
    selectedRecord = null;
    await refresh();
    showMessage(`revoked resolution ${applied.revoke.resolution_id}`, false);
  } catch (error) {
    revokeToken = "";
    $("revoke-button").textContent = "Preview revoke";
    $("revoke-button").classList.remove("danger");
    showMessage(error.message, true);
  }
});

$("lookup-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(event.target);
  const button = $("lookup-button");
  button.disabled = true;
  button.textContent = "Looking up…";
  try {
    const result = await api("/api/lookup", {
      method: "POST",
      body: JSON.stringify({
        part_number: form.get("part_number"),
        manufacturer: form.get("manufacturer"),
        quantity: Number(form.get("quantity") || "1"),
        providers: form.get("providers") || "auto",
      }),
    });
    renderCandidates(result.part);
  } catch (error) {
    showMessage(error.message, true);
  } finally {
    button.disabled = false;
    button.textContent = "Look up";
  }
});

function renderCandidates(part) {
  $("candidates").hidden = false;
  $("candidates-title").textContent =
    `Candidates for ${part.demand.manufacturer} ${part.demand.part_number} — status ${part.status}`;
  const issue = $("candidates-issue");
  issue.hidden = !part.issue_message;
  issue.textContent = part.issue_message
    ? `${part.issue_code}: ${part.issue_message}`
    : "";
  const body = $("candidates-body");
  body.replaceChildren();
  for (const offer of part.offers || []) {
    const row = document.createElement("tr");
    appendCell(row, offer.provider);
    appendCell(
      row,
      offer.review_required ? "review" : "safe",
      "badge " + (offer.review_required ? "review" : "active"),
    );
    appendCell(row, offer.manufacturer_part_number || "");
    appendCell(row, offer.distributor_part_number || "");
    appendCell(
      row,
      offer.available_quantity === undefined || offer.available_quantity === null
        ? "?"
        : String(offer.available_quantity),
    );
    const plan = offer.selected_plan || offer.candidate_plan;
    appendCell(row, plan ? `${plan.effective_unit_price} ${plan.currency}` : "?");
    const actionCell = document.createElement("td");
    const useButton = document.createElement("button");
    useButton.type = "button";
    useButton.className = "secondary";
    useButton.textContent = "Use";
    useButton.addEventListener("click", () => prefillApprove(part, offer));
    actionCell.appendChild(useButton);
    row.appendChild(actionCell);
    body.appendChild(row);
  }
  if ((part.offers || []).length === 0) {
    const row = document.createElement("tr");
    appendCell(row, "no candidate offers returned");
    body.appendChild(row);
  }
}

/* Seeds the approve form from a candidate. The approver field is never
   prefilled: choosing a row cannot clear engineering review. */
function prefillApprove(part, offer) {
  const form = $("approve-form");
  form.elements.manufacturer.value = part.demand.manufacturer;
  form.elements.part_number.value = part.demand.part_number;
  form.elements.replacement_manufacturer.value =
    offer.manufacturer || part.demand.manufacturer;
  form.elements.replacement_part_number.value =
    offer.manufacturer_part_number || part.demand.part_number;
  form.elements.provider.value = offer.provider || "";
  form.elements.provider_sku.value = offer.distributor_part_number || "";
  const notes = [`resolver: ${offer.provider} match ${offer.match_method}`];
  if (offer.available_quantity !== undefined && offer.available_quantity !== null) {
    notes.push(`stock ${offer.available_quantity}`);
  }
  const plan = offer.selected_plan || offer.candidate_plan;
  if (plan) notes.push(`unit ${plan.effective_unit_price} ${plan.currency}`);
  if (offer.review_required) notes.push("review-required match");
  form.elements.note.value = notes.join(", ");
  form.elements.approved_by.value = "";
  form.elements.approved_by.focus();
  form.scrollIntoView({ behavior: "smooth" });
}

$("include-inactive").addEventListener("change", () =>
  refresh().catch((error) => showMessage(error.message, true)),
);
$("refresh-button").addEventListener("click", () =>
  refresh().catch((error) => showMessage(error.message, true)),
);

refresh().catch((error) => showMessage(error.message, true));
