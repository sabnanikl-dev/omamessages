import QtQuick
import Quickshell
import Quickshell.Io
import "Model.js" as Model

// Talks to omamessagesd. The daemon merges every service into
// ~/.local/state/omarchy/omamessages/state.json and writes each opened thread
// to <service>/messages/<id>.json; this item watches those files and turns
// them into properties, and runs the daemon CLI for anything that needs to
// change (send, open, ...). Conversation IDs are namespaced: "<service>:<id>".
Item {
  id: root
  visible: false

  property var settings: ({})
  readonly property string pluginDir: String(Qt.resolvedUrl(".")).replace(/^file:\/\//, "").replace(/\/$/, "")
  readonly property string binary: pluginDir + "/bin/omamessagesd"
  readonly property string stateDir: (Quickshell.env("XDG_STATE_HOME") || (Quickshell.env("HOME") + "/.local/state")) + "/omarchy/omamessages"
  readonly property bool notifications: setting("notifications", true) !== false
  readonly property string enabledProviders: String(setting("providers", "gmessages,telegram,whatsapp"))
  // Until cutover, Google Messages is a read-only view of the old plugin's cache.
  readonly property string previewProviders: String(setting("preview", ""))

  // Mirrored from state.json
  property bool loaded: false
  property var providers: []
  property var conversations: []
  property var typing: []
  property int unreadCount: 0
  property bool binaryMissing: false

  // Mirrored from <service>/messages/<id>.json for the open conversation
  property string activeConversationId: ""
  property var messages: []
  property bool messagesLoading: false
  property bool messagesHasMore: false
  property string messagesError: ""

  // The tab the panel reopens on; kept in ui.json because plugin settings
  // are read-only at runtime.
  property string lastTab: "all"

  property string actionStatus: ""
  property string lastError: ""
  property bool busy: false

  signal conversationOpened(string id)

  readonly property bool anyConnected: providers.some(function(p) { return p.enabled && p.status === "connected" })
  readonly property bool anyEnabled: providers.some(function(p) { return p.enabled })

  function setting(name, fallback) {
    var value = settings ? settings[name] : undefined
    return value === undefined || value === null ? fallback : value
  }

  function safeName(id) {
    return String(id || "").replace(/[^A-Za-z0-9_-]/g, "_")
  }

  function provider(id) {
    for (var i = 0; i < providers.length; i++) if (providers[i].id === id) return providers[i]
    return null
  }

  function providerOf(convId) { return Model.providerOf(convId) }

  function providerName(id) {
    var p = provider(id)
    if (p) return p.name
    for (var i = 0; i < Model.tabs.length; i++) if (Model.tabs[i].id === id) return Model.tabs[i].label
    return id
  }

  function isPreview(providerId) {
    var p = provider(providerId)
    return !!(p && p.extra && p.extra.preview)
  }

  // A service can take replies once it's connected.
  function canSend(convId) {
    var p = provider(providerOf(convId))
    return !!(p && p.enabled && p.status === "connected")
  }

  function typingIn(convId) {
    var now = Date.now()
    for (var i = 0; i < typing.length; i++) {
      if (typing[i].conversationId === convId && typing[i].until > now) return true
    }
    return false
  }

  function conversation(id) {
    for (var i = 0; i < conversations.length; i++) if (conversations[i].id === id) return conversations[i]
    return null
  }

  // The bar tooltip and list header: one line for all services.
  function summaryLine(now) {
    if (binaryMissing) return Model.statusLine("missing")
    if (!loaded) return Model.statusLine("starting")
    var on = providers.filter(function(p) { return p.enabled && !(p.extra && p.extra.preview) })
    var previews = providers.filter(function(p) { return p.enabled && p.extra && p.extra.preview })
    var tail = previews.length > 0 ? " · " + previews.map(function(p) { return p.name }).join(", ") + " read-only" : ""
    if (on.length === 0) return previews.length > 0 ? tail.substring(3) : "No services enabled"
    return summaryFor(on, now) + tail
  }


  function summaryFor(on, now) {
    if (on.length === 1) return on[0].name + " · " + Model.statusLine(on[0].status, on[0].error, on[0].account, on[0].lastActivity, now)
    var connected = on.filter(function(p) { return p.status === "connected" }).length
    return connected + " of " + on.length + " services connected"
  }

  // --- daemon lifecycle ------------------------------------------------------

  property int daemonRestarts: 0

  Process {
    id: daemon
    command: ["sh", "-c", "mkdir -p \"$1\" && exec \"$2\" serve --providers \"$3\" --preview \"$4\"" + (root.notifications ? "" : " --no-notify") + " >> \"$1/daemon.log\" 2>&1", "sh", root.stateDir, root.binary, root.enabledProviders, root.previewProviders]
    running: false
    onExited: function(code, statusCode) {
      if (code === 0) return // another instance already owns the socket
      if (code === 127 || code === 126) { root.binaryMissing = true; return }
      if (root.daemonRestarts < 5) daemonRetry.restart()
      else root.lastError = "The messages daemon keeps exiting. See " + root.stateDir + "/daemon.log"
    }
  }

  Timer {
    id: daemonRetry
    interval: 5000
    onTriggered: { root.daemonRestarts += 1; daemon.running = true }
  }

  function ensureDaemon() {
    if (daemon.running || root.binaryMissing) return
    root.daemonRestarts = 0
    daemon.running = true
  }

  Timer {
    id: daemonBoot
    interval: 200
    running: true
    onTriggered: daemon.running = true
  }

  // --- state.json ------------------------------------------------------------

  FileView {
    id: stateFile
    path: root.stateDir + "/state.json"
    watchChanges: true
    printErrors: false
    onFileChanged: reload()
    onLoaded: root.applyState(text())
    onLoadFailed: function(err) { if (!root.loaded) stateRetry.restart() }
  }

  Timer {
    id: stateRetry
    interval: 1500
    onTriggered: stateFile.reload()
  }

  function applyState(content) {
    var parsed
    try { parsed = JSON.parse(String(content || "")) } catch (e) { return }
    if (!parsed || typeof parsed !== "object") return
    loaded = true
    providers = Array.isArray(parsed.providers) ? parsed.providers : []
    conversations = Array.isArray(parsed.conversations) ? parsed.conversations : []
    unreadCount = Number(parsed.unreadCount || 0)
    typing = Array.isArray(parsed.typing) ? parsed.typing : []
    if (typing.length > 0) typingExpiry.restart()
  }

  Timer {
    id: typingExpiry
    interval: 6500
    onTriggered: root.typing = root.typing.filter(function(t) { return t.until > Date.now() })
  }

  // --- ui.json ----------------------------------------------------------------

  FileView {
    id: uiFile
    path: root.stateDir + "/ui.json"
    atomicWrites: true
    printErrors: false
    onLoaded: {
      try {
        var parsed = JSON.parse(String(text() || ""))
        if (parsed && Model.tabs.some(function(t) { return t.id === parsed.lastTab })) root.lastTab = parsed.lastTab
      } catch (e) {}
    }
  }

  function setLastTab(tab) {
    if (!Model.tabs.some(function(t) { return t.id === tab })) return
    lastTab = tab
    uiFile.setText(JSON.stringify({ lastTab: tab }, null, 2) + "\n")
  }

  // --- <service>/messages/<id>.json ------------------------------------------

  FileView {
    id: messagesFile
    path: root.activeConversationId
      ? root.stateDir + "/" + Model.providerOf(root.activeConversationId) + "/messages/" + root.safeName(Model.nativeOf(root.activeConversationId)) + ".json"
      : ""
    watchChanges: true
    printErrors: false
    onFileChanged: reload()
    onLoaded: root.applyMessages(text())
    onLoadFailed: function(err) {
      // The daemon has not written this thread yet; opening it will.
      root.messages = []
      root.messagesHasMore = false
      root.messagesError = ""
    }
  }

  onActiveConversationIdChanged: {
    messages = []
    messagesHasMore = false
    messagesError = ""
    messagesLoading = activeConversationId !== ""
    if (activeConversationId !== "") {
      messagesFile.reload()
      run(["open", activeConversationId], function(ok, out, err) {
        root.messagesLoading = false
        if (!ok) root.messagesError = err
        // A thread opened for the first time had no file to watch yet.
        else messagesFile.reload()
      })
    }
  }

  function applyMessages(content) {
    var parsed
    try { parsed = JSON.parse(String(content || "")) } catch (e) { return }
    if (!parsed || parsed.conversationId !== Model.nativeOf(activeConversationId)) return
    messages = Array.isArray(parsed.messages) ? parsed.messages : []
    messagesLoading = parsed.loading === true
    messagesHasMore = parsed.hasMore === true
    messagesError = String(parsed.error || "")
  }

  // --- commands --------------------------------------------------------------

  property int running: 0

  Component {
    id: commandProcess
    Process {
      property var callback: null
      property string out: ""
      property string err: ""
      stdout: StdioCollector { onStreamFinished: out = text }
      stderr: StdioCollector { onStreamFinished: err = text }
      onExited: function(code) {
        root.running = Math.max(0, root.running - 1)
        root.busy = root.running > 0
        var errText = String(err || "").replace(/^error:\s*/, "").trim()
        if (code !== 0 && errText === "") errText = "command failed (" + code + ")"
        if (errText === "daemon is not running") root.ensureDaemon()
        if (callback) callback(code === 0, String(out || "").trim(), errText)
        destroy()
      }
    }
  }

  // Same as commandProcess, but hands the command something on stdin, so
  // codes, passwords and cookies never appear in the process list.
  Component {
    id: stdinCommandProcess
    Process {
      property var callback: null
      property string input: ""
      property string out: ""
      property string err: ""
      stdinEnabled: true
      stdout: StdioCollector { onStreamFinished: out = text }
      stderr: StdioCollector { onStreamFinished: err = text }
      // Stdin has to be closed, or the command waits for input forever.
      onStarted: { write(input); input = ""; stdinEnabled = false }
      onExited: function(code) {
        root.running = Math.max(0, root.running - 1)
        root.busy = root.running > 0
        var errText = String(err || "").replace(/^error:\s*/, "").trim()
        if (code !== 0 && errText === "") errText = "command failed (" + code + ")"
        if (errText === "daemon is not running") root.ensureDaemon()
        if (callback) callback(code === 0, String(out || "").trim(), errText)
        destroy()
      }
    }
  }

  function run(args, callback) {
    var proc = commandProcess.createObject(root, {
      command: [root.binary].concat(args),
      callback: callback || null
    })
    root.running += 1
    root.busy = true
    proc.running = true
  }

  // --- accounts ----------------------------------------------------------------

  // method "" is the service's usual way in; an alternative's method otherwise.
  function connect(providerId, method, callback) {
    lastError = ""
    actionStatus = "Starting…"
    var args = ["connect", providerId]
    if (method) args.push(method)
    run(args, function(ok, out, err) {
      actionStatus = ""
      if (!ok) lastError = err
      if (callback) callback(ok, err)
    })
  }

  function connectInput(providerId, value, callback) {
    lastError = ""
    var proc = stdinCommandProcess.createObject(root, {
      command: [root.binary, "connect-input", providerId],
      input: String(value || ""),
      callback: function(ok, out, err) {
        if (!ok) lastError = err
        if (callback) callback(ok, err)
      }
    })
    root.running += 1
    root.busy = true
    proc.running = true
  }

  function cancelConnect(providerId) {
    lastError = ""
    run(["cancel-connect", providerId])
  }

  function disconnect(providerId, callback) {
    lastError = ""
    if (Model.providerOf(activeConversationId) === providerId) activeConversationId = ""
    run(["disconnect", providerId], function(ok, out, err) {
      if (!ok) lastError = err
      if (callback) callback(ok, err)
    })
  }

  // Downloads an attachment without opening it (photos then show in full in
  // the chat, because the daemon records the file in the thread).
  function fetchMedia(convId, msgId, idx, done) {
    lastError = ""
    run(["fetch-media", convId, String(msgId), String(idx)], function(ok, out, err) {
      if (!ok) lastError = err
      if (done) done(ok)
    })
  }

  // Downloads an attachment through the daemon (once; later opens use the
  // cached file) and opens it with the desktop's default app.
  function fetchAndOpen(convId, msgId, idx, done) {
    lastError = ""
    run(["fetch-media", convId, String(msgId), String(idx)], function(ok, out, err) {
      if (ok && out) Quickshell.execDetached(["xdg-open", out])
      else lastError = err
      if (done) done(ok)
    })
  }

  // Opens a link from a message in the default browser or app. Only web
  // links are ever linked (see Model.linkify), so nothing else gets here.
  function openLink(url) {
    if (!/^https?:\/\//i.test(String(url))) return
    Quickshell.execDetached(["xdg-open", String(url)])
  }

  // --- attachments ------------------------------------------------------------

  readonly property string outboxDir: (Quickshell.env("XDG_CACHE_HOME") || (Quickshell.env("HOME") + "/.cache")) + "/omarchy/omamessages/outbox"

  // Runs any command (not the daemon) with the same bookkeeping as run().
  function runCommand(command, callback) {
    var proc = commandProcess.createObject(root, { command: command, callback: callback || null })
    root.running += 1
    root.busy = true
    proc.running = true
  }

  // The desktop file chooser; callback(paths), empty when cancelled.
  function pickFiles(callback) {
    lastError = ""
    run(["pick-files", "--multiple"], function(ok, out, err) {
      if (!ok) lastError = err
      var paths = ok && out ? out.split("\n").filter(function(p) { return p !== "" }) : []
      if (callback) callback(paths)
    })
  }

  // Saves an image on the clipboard to the outbox; callback(path), or ""
  // when the clipboard holds no image (so a text paste can go ahead).
  function pasteImage(callback) {
    var script = 'ty=$(wl-paste --list-types 2>/dev/null | grep -m1 "^image/") || exit 3; '
      + 'ext=${ty#image/}; [ "$ext" = jpeg ] && ext=jpg; mkdir -p "$1" || exit 1; '
      + 'f="$1/paste-$(date +%Y%m%d-%H%M%S)-$$.$ext"; wl-paste --no-newline --type "$ty" > "$f" && [ -s "$f" ] && echo "$f"'
    runCommand(["sh", "-c", script, "sh", root.outboxDir], function(ok, out, err) {
      if (callback) callback(ok ? String(out || "").trim() : "")
    })
  }

  // --- new chats -------------------------------------------------------------

  // Starts (or finds) a chat and sends text into it. callback(ok, id, err):
  // id is the namespaced chat to open, set even when only the text failed.
  function newChat(providerId, to, text, callback) {
    lastError = ""
    actionStatus = "Starting the chat…"
    var args = ["new", providerId, to]
    if (text) args.push(text)
    run(args, function(ok, out, err) {
      actionStatus = ""
      if (!ok) lastError = err
      if (callback) callback(ok, String(out || "").trim(), err)
    })
  }

  // callback(list): [{id, name, number}], empty on failure.
  function contacts(providerId, query, callback) {
    var args = ["contacts", providerId]
    if (query) args.push(query)
    run(args, function(ok, out, err) {
      var list = []
      if (ok) { try { list = JSON.parse(out) || [] } catch (e) {} }
      if (callback) callback(list)
    })
  }

  // providerId "" refreshes every enabled service.
  function refresh(providerId) {
    lastError = ""
    var args = ["refresh"]
    if (providerId) args.push(providerId)
    run(args, function(ok, out, err) { if (!ok) lastError = err })
  }

  function open(convId) {
    lastError = ""
    if (activeConversationId === convId) {
      messagesLoading = true
      run(["open", convId], function(ok, out, err) { messagesLoading = false; if (!ok) messagesError = err; else messagesFile.reload() })
    } else {
      activeConversationId = convId
    }
    conversationOpened(convId)
  }

  // Tells the chat's service the user is (or stopped) typing. Best effort:
  // failures aren't worth an error line.
  function setTyping(convId, on) {
    run(["typing", convId, on ? "on" : "off"])
  }

  // Marks a conversation read by re-opening it, without the loading state.
  function markRead(convId) {
    run(["open", convId])
  }

  function more(convId) {
    if (!convId || messagesLoading) return
    messagesLoading = true
    run(["more", convId], function(ok, out, err) { if (!ok) { messagesLoading = false; messagesError = err } })
  }

  function send(convId, text, files, callback) {
    lastError = ""
    var args = ["send", convId]
    for (var i = 0; files && i < files.length; i++) args.push("--file", files[i])
    args.push(text)
    run(args, function(ok, out, err) {
      if (!ok) lastError = err
      if (callback) callback(ok, err)
    })
  }
}
