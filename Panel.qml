// Omarchy Messages bar widget: every messaging service behind one icon.
import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui
import "components"
import "views"
import "Model.js" as Model

// One icon with a combined unread badge, one panel that flips between the
// tabbed inbox and a thread with a reply box.
Panel {
  id: root
  moduleName: "omamessages"
  ipcTarget: "omamessages"
  manageIpc: false

  // "list" | "thread" | "compose" | "accounts" | "connect"
  property string view: "list"
  onViewChanged: { clearBubbleSelection(); cursorIndex = 0; cursorActive = false }
  // The service the connect view is drawing.
  property string connectProvider: ""
  // Tabs for the services that are set up; the remembered tab (ui.json) falls
  // back to All while its service has no tab.
  readonly property var tabs: Model.visibleTabs(gm.providers, gm.conversations)
  readonly property string tab: tabs.some(function(t) { return t.id === gm.lastTab }) ? gm.lastTab : "all"
  property int cursorIndex: 0
  property bool cursorActive: false

  readonly property color foreground: (bar && bar.foreground !== undefined) ? bar.foreground : Color.foreground
  readonly property color accent: Color.accent
  readonly property color urgent: (bar && bar.urgent !== undefined) ? bar.urgent : Color.urgent
  readonly property color dim: Qt.darker(foreground, 1.55)
  readonly property color faint: Qt.darker(foreground, 2.2)
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family
  readonly property bool showPreview: root.setting("showPreview", true) !== false
  readonly property var service: gm
  readonly property var activeConversation: gm.conversation(gm.activeConversationId)
  property double now: Date.now()
  readonly property string summaryLine: gm.summaryLine(root.now)

  // Keeps the "seen N s ago" text moving while the panel is open.
  Timer {
    interval: 5000
    repeat: true
    running: root.opened
    triggeredOnStart: true
    onTriggered: root.now = Date.now()
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  onOpenedChanged: {
    if (opened) {
      cursorActive = false
      if (view === "list" && gm.anyConnected) gm.refresh("")
      if (view === "thread" && gm.activeConversationId) gm.open(gm.activeConversationId)
      Qt.callLater(focusForView)
    }
  }

  function focusForView() {
    if (!opened) return
    if (view === "thread") threadView.focusReply()
    else if (view === "connect" && connectView.kind === "input") connectView.inputTarget.forceActiveFocus()
    else if (view === "compose") composeView.recipientInput.forceActiveFocus()
    else keyCatcher.forceActiveFocus()
  }

  function setTab(t) {
    if (tab === t) return
    gm.setLastTab(t)
    cursorIndex = 0
    cursorActive = false
  }

  // direction +1/-1 through the tab strip, wrapping at the ends.
  function cycleTab(direction) {
    var i = 0
    for (var j = 0; j < tabs.length; j++) if (tabs[j].id === tab) i = j
    var n = tabs.length
    setTab(tabs[(i + direction + n) % n].id)
  }

  // New message, starting on the current tab's service.
  function showCompose() {
    composeView.reset(tab)
    view = "compose"
    Qt.callLater(focusForView)
  }

  function showAccounts() {
    view = "accounts"
    Qt.callLater(focusForView)
  }

  function showConnect(providerId) {
    connectProvider = providerId
    view = "connect"
    Qt.callLater(focusForView)
  }

  function startConnect(providerId, method) {
    showConnect(providerId)
    gm.connect(providerId, method)
  }

  // Leaving the connect view early abandons the flow.
  function cancelConnect() {
    var p = gm.provider(connectProvider)
    if (p && p.status !== "connected") gm.cancelConnect(connectProvider)
    showAccounts()
  }

  function showList() {
    view = "list"
    cursorActive = false
    Qt.callLater(focusForView)
  }

  function openConversation(id) {
    if (!id) return
    view = "thread"
    gm.open(id)
    threadView.clearReply()
    threadView.followLatest()
    Qt.callLater(focusForView)
  }

  function goBack() {
    if (view === "list") root.close()
    else if (view === "connect") cancelConnect()
    else showList()
  }

  function moveCursor(dy) {
    if (view !== "list" && view !== "accounts") return
    cursorActive = true
    var n = view === "list" ? inboxView.model.length : accountsView.rows.length
    if (n === 0) return
    cursorIndex = Math.max(0, Math.min(n - 1, cursorIndex + dy))
    if (view === "list") inboxView.scrollTo(cursorIndex)
  }

  function activateCursor() {
    if (view === "accounts") { if (cursorActive) accountsView.activate(cursorIndex); return }
    if (view !== "list") return
    var list = inboxView.model
    if (list.length === 0) return
    openConversation(list[Math.max(0, Math.min(cursorIndex, list.length - 1))].id)
  }

  Service {
    id: gm
    settings: root.settings
    // A message arriving in the thread on screen has been seen.
    onConversationsChanged: if (root.opened && root.view === "thread") autoRead.restart()
    // A finished connect goes back to Accounts after a moment to show it.
    onProvidersChanged: {
      var p = gm.provider(root.connectProvider)
      if (root.view === "connect" && p && p.status === "connected") connectDone.restart()
    }
  }

  Timer {
    id: connectDone
    interval: 1200
    onTriggered: if (root.view === "connect") root.showAccounts()
  }

  Timer {
    id: autoRead
    interval: 800
    onTriggered: {
      var c = root.activeConversation
      if (root.opened && root.view === "thread" && c && c.unread && gm.canSend(c.id)) gm.markRead(c.id)
    }
  }

  IpcHandler {
    target: root.ipcTarget
    function open(): void { root.open() }
    function close(): void { root.close() }
    function show(): void { root.open() }
    function hide(): void { root.close() }
    function toggle(): void { root.toggle() }
    function refresh(): string { gm.refresh(""); return "ok" }
    function tab(id: string): string { root.setTab(id); root.showList(); root.open(); return "ok" }
    function openConversation(id: string): string { root.openConversation(id); root.open(); return "ok" }
    function accounts(): string { root.showAccounts(); root.open(); return "ok" }
    function compose(): string { root.showCompose(); root.open(); return "ok" }
    function connectService(provider: string): string { root.startConnect(provider, ""); root.open(); return "ok" }
    function status(): string { return root.summaryLine }
    function unread(): string { return String(gm.unreadCount) }
  }

  // --- bar icon ---------------------------------------------------------------

  // The panel closes as soon as another window (say, the file manager) takes
  // focus, so files are dropped on the bar icon instead: they attach to the
  // chat that was open, and the panel reopens on it.
  DropArea {
    id: iconDrop
    anchors.fill: parent
    keys: ["text/uri-list"]
    enabled: threadView.canAttach && gm.activeConversationId !== ""
    onDropped: function(drop) {
      if (threadView.stageUrls(drop.urls) > 0) {
        root.view = "thread"
        root.open()
      }
      drop.accept()
    }
  }

  Rectangle {
    visible: iconDrop.containsDrag
    anchors.fill: parent
    radius: Style.space(4)
    color: "transparent"
    border.width: Style.space(2)
    border.color: root.accent
    z: 2
  }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: gm.anyConnected ? "󰍦" : "󰍥"
    active: gm.unreadCount > 0
    dimmed: !gm.anyConnected
    tooltipText: gm.unreadCount > 0
      ? (gm.unreadCount + " unread " + (gm.unreadCount === 1 ? "conversation" : "conversations"))
      : root.summaryLine
    onPressed: function(buttonCode) {
      if (buttonCode === Qt.RightButton) gm.refresh("")
      else if (buttonCode === Qt.MiddleButton) { root.showCompose(); root.open() }
      else root.toggle()
    }

    Rectangle {
      visible: gm.unreadCount > 0
      anchors.right: parent.right
      anchors.top: parent.top
      anchors.rightMargin: Style.space(2)
      anchors.topMargin: Style.space(2)
      width: Math.max(Style.space(11), badge.implicitWidth + Style.space(5))
      height: Style.space(11)
      radius: height / 2
      color: root.urgent

      Text {
        textFormat: Text.PlainText
        id: badge
        anchors.centerIn: parent
        text: gm.unreadCount > 9 ? "9+" : String(gm.unreadCount)
        color: Color.background
        font.family: root.fontFamily
        font.pixelSize: Style.space(8)
        font.bold: true
      }
    }
  }

  // --- panel --------------------------------------------------------------------

  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    // KeyboardPanel focuses this once the surface maps, after anything we
    // schedule ourselves, so it has to be the reply box when a thread is up.
    focusTarget: root.view === "thread" ? threadView.replyInput
      : root.view === "compose" ? composeView.recipientInput
      : root.view === "connect" && connectView.kind === "input" ? connectView.inputTarget
      : keyCatcher
    contentWidth: panel.fittedContentWidth(Style.space(420))
    // The list hugs its content; a thread gets a tall fixed card.
    readonly property real listChrome: Style.space(150)
    contentHeight: root.view === "list"
      ? panel.fittedContentHeight(Math.max(Style.space(260), inboxView.listHeight + listChrome), Style.space(640))
      : panel.fittedContentHeight(Style.space(640), Style.space(720))

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      blocked: threadView.replyFocused || connectView.inputFocused || composeView.inputFocused
      onMoveRequested: function(dx, dy) {
        if (root.view !== "list" && root.view !== "accounts") return
        if (!root.cursorActive) { root.cursorActive = true; return }
        root.moveCursor(dy)
      }
      onActivateRequested: root.activateCursor()
      onCloseRequested: root.goBack()
      onTabRequested: function(direction) {
        if (root.view === "list") root.cycleTab(direction)
        else root.switchPanel(direction)
      }
      onTextKey: function(t) {
        if (root.view !== "list") return
        if (t === "r" || t === "R") gm.refresh("")
        else if (t === ",") root.showAccounts()
        else if (t === "n" || t === "N") root.showCompose()
        else if (t >= "1" && t <= String(root.tabs.length)) root.setTab(root.tabs[Number(t) - 1].id)
      }

      ColumnLayout {
        anchors.fill: parent
        spacing: Style.space(10)

        // Header ---------------------------------------------------------------
        RowLayout {
          Layout.fillWidth: true
          spacing: Style.space(8)

          PanelActionButton {
            visible: root.view !== "list"
            iconText: "󰁍"
            tooltipText: "Back"
            foreground: root.foreground
            fontFamily: root.fontFamily
            onClicked: root.goBack()
          }

          Rectangle {
            visible: !!(root.view === "thread" && root.activeConversation)
            width: Style.space(30)
            height: Style.space(30)
            radius: width / 2
            color: Qt.rgba(root.accent.r, root.accent.g, root.accent.b, 0.18)
            Text {
              textFormat: Text.PlainText
              anchors.centerIn: parent
              text: root.activeConversation ? Model.initials(root.activeConversation.name) : ""
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              font.bold: true
            }
            ServiceMark {
              provider: root.activeConversation ? root.activeConversation.provider : ""
              fontFamily: root.fontFamily
              anchors.right: parent.right
              anchors.bottom: parent.bottom
              anchors.rightMargin: -Style.space(3)
              anchors.bottomMargin: -Style.space(3)
            }
          }

          ColumnLayout {
            Layout.fillWidth: true
            spacing: Style.space(1)

            Text {
              textFormat: Text.PlainText
              Layout.fillWidth: true
              text: root.view === "thread" && root.activeConversation ? root.activeConversation.name
                  : root.view === "compose" ? "New message"
                  : root.view === "accounts" ? "Accounts"
                  : root.view === "connect" ? "Connect " + Model.serviceName(root.connectProvider, gm.providerName(root.connectProvider))
                  : "Messages"
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.heading
              font.bold: true
              elide: Text.ElideRight
            }

            Text {
              textFormat: Text.PlainText
              Layout.fillWidth: true
              text: {
                if (root.view === "thread" && root.activeConversation) {
                  var c = root.activeConversation
                  var parts = [gm.providerName(c.provider)]
                  if (c.extra && c.extra.type) parts.push(String(c.extra.type).toUpperCase())
                  if (gm.typingIn(c.id)) parts.push("typing…")
                  else {
                    var others = (c.participants || []).filter(function(p) { return !p.isMe })
                    if (others.length === 1 && others[0].number && others[0].number !== c.name) parts.push(others[0].number)
                    else if (others.length > 1) parts.push(others.length + " people")
                  }
                  return parts.join(" · ")
                }
                return root.summaryLine
              }
              color: root.dim
              font.family: root.fontFamily
              font.pixelSize: Style.font.caption
              elide: Text.ElideRight
            }
          }

          PanelActionButton {
            visible: gm.anyEnabled && (root.view === "list" || root.view === "thread")
            iconText: "󰑐"
            tooltipText: root.view === "thread" ? "Reload thread" : "Refresh (r)"
            foreground: root.foreground
            fontFamily: root.fontFamily
            onClicked: root.view === "thread" ? gm.open(gm.activeConversationId) : gm.refresh("")
          }

          PanelActionButton {
            visible: root.view === "list"
            iconText: "󰏫"
            tooltipText: "New message (n)"
            foreground: root.foreground
            fontFamily: root.fontFamily
            onClicked: root.showCompose()
          }

          PanelActionButton {
            visible: root.view === "list"
            iconText: "󰒓"
            tooltipText: "Accounts (,)"
            foreground: root.foreground
            fontFamily: root.fontFamily
            onClicked: root.showAccounts()
          }
        }

        // Error / action line ----------------------------------------------------
        Text {
          textFormat: Text.PlainText
          visible: gm.actionStatus !== "" || gm.lastError !== ""
          Layout.fillWidth: true
          text: gm.actionStatus !== "" ? gm.actionStatus : gm.lastError
          color: gm.lastError !== "" && gm.actionStatus === "" ? root.urgent : root.dim
          font.family: root.fontFamily
          font.pixelSize: Style.font.bodySmall
          wrapMode: Text.WordWrap
        }

        PanelSeparator { Layout.fillWidth: true; foreground: root.foreground }

        // Body ---------------------------------------------------------------------
        Item {
          Layout.fillWidth: true
          Layout.fillHeight: true

          Text {
            textFormat: Text.PlainText
            anchors.centerIn: parent
            width: parent.width - Style.space(40)
            visible: gm.binaryMissing
            text: "The daemon binary is missing. Run build.sh in the plugin folder (needs Go)."
            color: root.urgent
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            wrapMode: Text.WordWrap
            horizontalAlignment: Text.AlignHCenter
          }

          InboxView {
            id: inboxView
            anchors.fill: parent
            visible: root.view === "list" && !gm.binaryMissing
            ui: root
            gm: root.service
            tab: root.tab
          }

          ThreadView {
            id: threadView
            anchors.fill: parent
            visible: root.view === "thread"
            ui: root
            gm: root.service
            conv: root.activeConversation
          }

          ComposeView {
            id: composeView
            anchors.fill: parent
            visible: root.view === "compose"
            ui: root
            gm: root.service
          }

          AccountsView {
            id: accountsView
            anchors.fill: parent
            visible: root.view === "accounts"
            ui: root
            gm: root.service
          }

          ConnectView {
            id: connectView
            anchors.fill: parent
            visible: root.view === "connect"
            ui: root
            gm: root.service
            providerId: root.connectProvider
          }
        }

        // Footer hints -----------------------------------------------------------
        Text {
          textFormat: Text.PlainText
          Layout.fillWidth: true
          visible: root.view === "list" || root.view === "accounts" || root.view === "connect"
          text: root.view === "accounts" ? "j/k move · Enter connect/disconnect · Esc back"
              : root.view === "connect" ? "Esc cancel"
              : (root.tabs.length > 1 ? "1–" + root.tabs.length + " / Tab switch · " : "") + "j/k move · Enter open · n new · , accounts · r refresh · Esc close"
          color: root.faint
          font.family: root.fontFamily
          font.pixelSize: Style.font.caption
          elide: Text.ElideRight
        }
      }
    }
  }

  // The bubble whose body text currently holds a highlight (one at a time).
  property var selectedBubble: null

  function clearBubbleSelection() {
    if (selectedBubble) selectedBubble.clearSelection()
    selectedBubble = null
  }

  function copyToClipboard(text) {
    copyProc.command = ["sh", "-c", "printf '%s' \"$1\" | wl-copy", "sh", text || ""]
    copyProc.running = true
  }

  Process { id: copyProc }
}
