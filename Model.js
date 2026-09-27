.pragma library

function pad(n) { return n < 10 ? "0" + n : "" + n }

function sameDay(a, b) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

function clock(ms) {
  var d = new Date(ms)
  var h = d.getHours()
  var ap = h >= 12 ? "pm" : "am"
  h = h % 12
  if (h === 0) h = 12
  return h + ":" + pad(d.getMinutes()) + " " + ap
}

// Short timestamp for the conversation list: clock today, weekday this week,
// otherwise a date.
function listTime(ms) {
  if (!ms) return ""
  var d = new Date(ms)
  var now = new Date()
  if (sameDay(d, now)) return clock(ms)
  var diff = now.getTime() - ms
  if (diff < 6 * 86400000) return Qt.formatDate(d, "ddd")
  if (d.getFullYear() === now.getFullYear()) return Qt.formatDate(d, "d MMM")
  return Qt.formatDate(d, "d MMM yyyy")
}

function dayLabel(ms) {
  var d = new Date(ms)
  var now = new Date()
  if (sameDay(d, now)) return "Today"
  var y = new Date(now.getTime() - 86400000)
  if (sameDay(d, y)) return "Yesterday"
  if (now.getTime() - ms < 6 * 86400000) return Qt.formatDate(d, "dddd")
  return Qt.formatDate(d, "dddd d MMMM")
}

function initials(name) {
  var s = String(name || "").trim()
  if (!s) return "?"
  if (/^[+\d(]/.test(s)) return "#"
  var parts = s.split(/\s+/)
  var out = parts[0].charAt(0)
  if (parts.length > 1) out += parts[parts.length - 1].charAt(0)
  return out.toUpperCase()
}

function oneLine(text) {
  return String(text || "").replace(/\s+/g, " ").trim()
}

function statusGlyph(status) {
  switch (status) {
    case "sending": return "󰔟"
    case "sent": return "󰄬"
    case "delivered": return "󰄭"
    case "read": return "󰄭"
    case "failed": return "󰅙"
    default: return ""
  }
}

function attachmentGlyph(kind) {
  switch (kind) {
    case "image": return "󰋩"
    case "video": return "󰕧"
    case "audio": return "󰎈"
    default: return "󰈔"
  }
}

function ago(ms, now) {
  if (!ms) return ""
  var s = Math.max(0, Math.round(((now || Date.now()) - ms) / 1000))
  if (s < 5) return "just now"
  if (s < 60) return s + "s ago"
  var m = Math.round(s / 60)
  if (m < 60) return m + " min ago"
  var h = Math.round(m / 60)
  if (h < 24) return h + " h ago"
  return Math.round(h / 24) + " d ago"
}

// One service's status for the header and tooltips.
function statusLine(status, error, account, lastActivity, now) {
  switch (status) {
    case "disconnected": return error ? error : "Not connected"
    case "pairing": return "Connecting…"
    case "connecting": return "Connecting…"
    case "connected": {
      var parts = ["Connected"]
      if (account) parts.push(account)
      if (lastActivity) parts.push("seen " + ago(lastActivity, now))
      return parts.join(" · ")
    }
    case "phone_offline": return "Phone is not responding" + (lastActivity ? " · last seen " + ago(lastActivity, now) : "")
    case "error": return error ? error : "Connection error"
    case "missing": return "Daemon binary missing"
    case "starting": return "Starting…"
  }
  return status || ""
}

// The tabs across the top of the panel, in order. "all" merges everything.
var tabs = [
  { id: "all", label: "All" },
  { id: "gmessages", label: "Messages" },
  { id: "telegram", label: "Telegram" },
  { id: "whatsapp", label: "WhatsApp" }
]

// The real services, in tab order, with the name the Accounts screen uses.
var services = [
  { id: "gmessages", label: "Messages", full: "Google Messages" },
  { id: "telegram", label: "Telegram", full: "Telegram" },
  { id: "whatsapp", label: "WhatsApp", full: "WhatsApp" }
]

function serviceName(id, fallback) {
  for (var i = 0; i < services.length; i++) if (services[i].id === id) return services[i].full
  return fallback || id
}

// Tabs appear only for services that are on and set up: connected, on their
// way, in trouble, or still holding chats. A service that is simply signed
// out lives on the Accounts screen until it connects.
function visibleTabs(providers, conversations) {
  var out = [tabs[0]]
  for (var i = 1; i < tabs.length; i++) {
    var id = tabs[i].id
    var p = null
    for (var j = 0; j < providers.length; j++) if (providers[j].id === id) p = providers[j]
    if (!p || !p.enabled) continue
    var hasChats = conversations.some(function(c) { return c.provider === id })
    if (p.status !== "disconnected" || hasChats) out.push(tabs[i])
  }
  return out
}

// The dot color on the Accounts screen: ok, busy, trouble, or off.
function statusTone(status) {
  switch (status) {
    case "connected": return "ok"
    case "pairing": case "connecting": return "busy"
    case "phone_offline": case "error": return "trouble"
  }
  return "off"
}

// The provider part of a namespaced conversation ID ("telegram:-100" -> "telegram").
function providerOf(id) {
  var s = String(id || "")
  var i = s.indexOf(":")
  return i > 0 ? s.substring(0, i) : ""
}

// The provider's own ID ("telegram:-100" -> "-100"). Only the first colon
// separates, the same as the daemon's SplitID.
function nativeOf(id) {
  var s = String(id || "")
  var i = s.indexOf(":")
  return i > 0 ? s.substring(i + 1) : s
}

// The letter and color of a service's small badge.
function providerMark(provider) {
  switch (provider) {
    case "gmessages": return { letter: "G", color: "#6fa8dc" }
    case "telegram": return { letter: "T", color: "#5fb3d9" }
    case "whatsapp": return { letter: "W", color: "#6abf69" }
  }
  return { letter: String(provider || "?").charAt(0).toUpperCase(), color: "#8a8a8d" }
}

function filterByTab(conversations, tab) {
  if (!tab || tab === "all") return conversations
  return conversations.filter(function(c) { return c.provider === tab })
}

function formatSize(bytes) {
  var n = Number(bytes || 0)
  if (n < 1024) return n + " B"
  if (n < 1024 * 1024) return Math.round(n / 1024) + " KB"
  return (n / (1024 * 1024)).toFixed(1) + " MB"
}

function normalizeNumber(raw) {
  var s = String(raw || "").replace(/[\s\-().]/g, "")
  return s
}

function looksLikeNumber(raw) {
  return /^\+?\d{3,15}$/.test(normalizeNumber(raw))
}

function escapeHtml(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;")
}

// Message text as rich text with clickable links, or "" when it has no
// links (the bubble then shows it as plain text). Only http(s) and www.
// addresses are linked; punctuation that ends a sentence stays outside the
// link, and a closing bracket only belongs to it when it opened one.
function linkify(text, color) {
  var s = String(text || "")
  var re = /\b(?:https?:\/\/|www\.)[^\s<>"]+/gi
  var out = ""
  var last = 0
  var found = false
  var m
  while ((m = re.exec(s)) !== null) {
    var url = m[0]
    while (/[.,;:!?'"]$/.test(url) || (/[)\]]$/.test(url) && (url.split(url.slice(-1) === ")" ? "(" : "[").length - 1) < (url.split(url.slice(-1)).length - 1)))
      url = url.slice(0, -1)
    if (url.length < 5) continue
    var href = /^www\./i.test(url) ? "https://" + url : url
    out += plainHtml(s.substring(last, m.index))
    out += '<a href="' + escapeHtml(href) + '" style="color:' + color + '">' + escapeHtml(url) + "</a>"
    last = m.index + url.length
    re.lastIndex = last
    found = true
  }
  if (!found) return ""
  return out + plainHtml(s.substring(last))
}

// Plain text as rich text that looks the same: escaped, line breaks kept,
// runs of spaces kept.
function plainHtml(s) {
  return escapeHtml(s).replace(/\n/g, "<br>").replace(/  /g, " &nbsp;")
}

