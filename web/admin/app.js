"use strict";

const labels = {
  "control-plane": "Control plane",
  subscription: "Subscription",
  delivery: "Delivery",
  billing: "Billing",
  telemetry: "Telemetry",
  orchestrator: "Orchestrator",
  active: "Активные",
  suspended: "Приостановлены",
  expired: "Истекли",
  banned: "Заблокированы",
  trialing: "Пробные",
  past_due: "Просрочены",
  canceled: "Отменены",
  pending: "Ожидают",
  settled: "Оплачены",
  invalid: "Некорректные"
};

const byId = (id) => document.getElementById(id);
const text = (value) => value === null || value === undefined || value === "" ? "-" : String(value);
const label = (value) => labels[value] || text(value);

function clearElement(element) {
  while (element.firstChild) element.removeChild(element.firstChild);
}

function cell(row, value) {
  const element = document.createElement("td");
  element.textContent = text(value);
  row.appendChild(element);
}

function statusCell(row, value) {
  const wrapper = document.createElement("td");
  const badge = document.createElement("span");
  badge.className = `status ${text(value)}`;
  badge.textContent = label(value);
  wrapper.appendChild(badge);
  row.appendChild(wrapper);
}

function resetView() {
  ["health", "fleet", "accounts", "subscriptions", "billing-counts", "billing-recent", "plans"].forEach((id) => clearElement(byId(id)));
  ["fleet-error", "accounts-error", "subscriptions-error", "billing-error", "plans-error", "health-summary"].forEach((id) => { byId(id).textContent = ""; });
  ["fleet-empty", "billing-empty", "plans-empty", "demo", "global-error"].forEach((id) => { byId(id).hidden = true; });
  ["fleet-table", "billing-table"].forEach((id) => { byId(id).hidden = false; });
  byId("updated").textContent = "-";
}

function renderMetrics(target, values) {
  Object.entries(values || {}).forEach(([name, value]) => {
    const item = document.createElement("div");
    item.className = "metric";
    const caption = document.createElement("span");
    caption.textContent = label(name);
    const count = document.createElement("strong");
    count.textContent = text(value);
    item.append(caption, count);
    target.appendChild(item);
  });
}

function renderHealth(values) {
  let healthy = 0;
  Object.entries(values || {}).forEach(([name, value]) => {
    const item = document.createElement("div");
    item.className = "health-item";
    const title = document.createElement("strong");
    title.textContent = label(name);
    const dot = document.createElement("span");
    dot.className = value.ok ? "dot ok" : "dot";
    dot.title = value.ok ? "Доступен" : `Недоступен, HTTP ${value.status || 0}`;
    item.append(title, dot);
    byId("health").appendChild(item);
    if (value.ok) healthy += 1;
  });
  const total = Object.keys(values || {}).length;
  byId("health-summary").textContent = `${healthy} из ${total} доступны`;
}

function renderFleet(items) {
  byId("fleet-empty").hidden = items.length !== 0;
  items.forEach((node) => {
    const row = document.createElement("tr");
    cell(row, node.id);
    cell(row, node.region);
    cell(row, node.role);
    statusCell(row, node.status);
    cell(row, (node.transports || []).join(", "));
    cell(row, node.created_at);
    byId("fleet").appendChild(row);
  });
}

function renderBilling(value) {
  renderMetrics(byId("billing-counts"), value.counts || {});
  const recent = value.recent || [];
  byId("billing-empty").hidden = recent.length !== 0;
  recent.forEach((invoice) => {
    const row = document.createElement("tr");
    cell(row, invoice.invoice_id);
    cell(row, invoice.anon_user_id);
    cell(row, invoice.plan);
    statusCell(row, invoice.status);
    cell(row, `${text(invoice.amount)} ${text(invoice.currency)}`);
    cell(row, invoice.created_at);
    cell(row, invoice.expires_at);
    byId("billing-recent").appendChild(row);
  });
}

function durationDays(seconds) {
  return Number.isInteger(seconds) && seconds > 0 ? `${Math.round(seconds / 86400)} дн.` : "-";
}

function renderPlans(items) {
  byId("plans-empty").hidden = items.length !== 0;
  items.forEach((plan) => {
    const card = document.createElement("article");
    card.className = "plan";
    const title = document.createElement("h3");
    title.textContent = text(plan.id);
    const duration = document.createElement("p");
    duration.textContent = `Срок: ${durationDays(plan.duration_seconds)}`;
    const grace = document.createElement("p");
    grace.textContent = `Grace: ${durationDays(plan.grace_seconds)}`;
    const price = document.createElement("p");
    price.textContent = Object.entries(plan.prices || {}).map(([currency, amount]) => `${amount} ${currency}`).join(", ") || "Цена не задана";
    card.append(title, duration, grace, price);
    byId("plans").appendChild(card);
  });
}

function render(data) {
  const errors = data.errors || {};
  byId("updated").textContent = text(data.generated_at);
  if (data.mode === "demo") {
    byId("demo").textContent = data.demo_notice || "Демонстрационные данные";
    byId("demo").hidden = false;
  }
  renderHealth(data.health || {});
  if (!errors.fleet) renderFleet(data.fleet || []);
  else byId("fleet-table").hidden = true;
  if (!errors.accounts) {
    renderMetrics(byId("accounts"), data.accounts || {});
    renderMetrics(byId("subscriptions"), data.subscriptions || {});
  }
  if (!errors.billing) renderBilling(data.billing || { counts: {}, recent: [] });
  else byId("billing-table").hidden = true;
  if (!errors.plans) renderPlans(data.plans || []);
  Object.entries(errors).forEach(([section, message]) => {
    const target = byId(`${section}-error`);
    if (target) target.textContent = text(message);
    if (section === "accounts") byId("subscriptions-error").textContent = text(message);
  });
}

async function loadOverview() {
  const button = byId("retry");
  button.disabled = true;
  resetView();
  byId("loading").hidden = false;
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), 7000);
  try {
    const response = await fetch("/api/overview", { cache: "no-store", credentials: "same-origin", signal: controller.signal });
    if (!response.ok) throw new Error("request failed");
    render(await response.json());
  } catch (_) {
    byId("global-error").hidden = false;
  } finally {
    window.clearTimeout(timer);
    byId("loading").hidden = true;
    button.disabled = false;
  }
}

byId("retry").addEventListener("click", loadOverview);
loadOverview();
