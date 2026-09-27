import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import qs.Commons
import qs.Ui
import "../components"
import "../Model.js" as Model

// One conversation: its messages, a typing line, and the reply box. It looks
// the same on every service; the placeholder names where the reply goes.
ColumnLayout {
  id: thread
  required property var ui
  required property var gm
  property var conv: null
  readonly property string serviceName: conv ? gm.providerName(conv.provider) : ""
  readonly property bool canSend: conv ? gm.canSend(conv.id) : false
  // Showing the other side we're typing: sent at most every 4 s while the
  // text changes, cleared 5 s after the last change, on send, and on leaving.
  readonly property bool canType: {
    var p = conv ? gm.provider(conv.provider) : null
    return !!(p && p.caps && p.caps.typing && p.status === "connected")
  }
  property string typingConv: ""
  property double typingSentAt: 0

  function noteTyping() {
    if (!canType || !conv) return
    if (replyField.text.trim() === "") { stopTyping(); return }
    var now = Date.now()
    if (typingConv !== conv.id || now - typingSentAt > 4000) {
      if (typingConv !== "" && typingConv !== conv.id) gm.setTyping(typingConv, false)
      gm.setTyping(conv.id, true)
      typingConv = conv.id
      typingSentAt = now
    }
    typingStop.restart()
  }

  function stopTyping() {
    typingStop.stop()
    if (typingConv !== "") gm.setTyping(typingConv, false)
    typingConv = ""
    typingSentAt = 0
  }

  Timer {
    id: typingStop
    interval: 5000
    onTriggered: thread.stopTyping()
  }

  // Files waiting to be sent with the next message (local paths).
  property var staged: []
  readonly property bool canAttach: {
    var p = conv ? gm.provider(conv.provider) : null
    return !!(p && p.caps && p.caps.attachments && p.status === "connected")
  }
  readonly property bool fetchable: {
    var p = conv ? gm.provider(conv.provider) : null
    return !!(p && p.caps && p.caps.fetchMedia && p.status === "connected")
  }
  readonly property bool replyFocused: replyField.activeFocus
  readonly property Item replyInput: replyField
  property bool sending: false

  spacing: Style.space(8)

  function focusReply() { replyField.forceActiveFocus() }
  function clearReply() { replyField.text = "" }

  // The thread follows new messages only while its bottom is in view; once
  // the reader scrolls up, nothing moves it (a thumbnail arriving, a file
  // being opened, the panel reopening) until they come back down.
  property bool followBottom: true
  property bool settingY: false
  property real lastContentHeight: 0
  // Older messages were just added above: keep the reader's place.
  property bool olderAdded: false
  property string firstId: ""
  readonly property string convKey: conv ? conv.id : ""
  onConvKeyChanged: { stopTyping(); staged = []; followLatest() }

  // A chat opened (or re-picked from the list) starts at its newest message.
  function stage(paths) {
    var next = staged.slice()
    for (var i = 0; i < paths.length; i++) if (paths[i] && next.indexOf(paths[i]) < 0) next.push(paths[i])
    staged = next
  }

  // file:// URLs from a drag (the bar icon or the thread) become staged paths.
  function stageUrls(urls) {
    var paths = []
    for (var i = 0; i < urls.length; i++) {
      var u = String(urls[i])
      if (u.indexOf("file://") === 0) paths.push(decodeURIComponent(u.replace(/^file:\/\/(localhost)?/, "")))
    }
    stage(paths)
    return paths.length
  }

  function unstage(path) {
    staged = staged.filter(function(p) { return p !== path })
  }

  // Ctrl+V: an image on the clipboard becomes an attachment; anything else
  // pastes as text.
  function pasteIntoReply() {
    if (!canAttach) { replyField.paste(); return }
    gm.pasteImage(function(path) {
      if (path) thread.stage([path])
      else replyField.paste()
    })
  }

  function followLatest() {
    followBottom = true
    firstId = ""
    olderAdded = false
    scrollToBottom()
  }

  function setY(y) {
    settingY = true
    threadFlick.contentY = Math.max(0, Math.min(y, Math.max(0, threadFlick.contentHeight - threadFlick.height)))
    settingY = false
  }

  function scrollToBottom() {
    Qt.callLater(function() { thread.setY(threadFlick.contentHeight - threadFlick.height) })
  }

  Connections {
    target: thread.gm
    function onMessagesChanged() {
      var ms = thread.gm.messages
      var first = ms.length > 0 ? ms[0].id : ""
      if (thread.firstId !== "" && first !== thread.firstId && ms.some(function(m) { return m.id === thread.firstId }))
        thread.olderAdded = true
      thread.firstId = first
    }
  }

  function sendReply() {
    var text = replyField.text.trim()
    var files = staged.slice()
    if ((!text && files.length === 0) || !conv || sending) return
    stopTyping()
    sending = true
    followBottom = true
    var convId = conv.id
    replyField.text = ""
    staged = []
    gm.send(convId, text, files, function(ok, err) {
      thread.sending = false
      if (!ok) { replyField.text = text; thread.staged = files }
      Qt.callLater(function() { replyField.forceActiveFocus() })
    })
  }

  Flickable {
    id: threadFlick
    Layout.fillWidth: true
    Layout.fillHeight: true
    contentWidth: width
    contentHeight: threadColumn.implicitHeight
    clip: true
    boundsBehavior: Flickable.StopAtBounds
    flickableDirection: Flickable.VerticalFlick
    interactive: contentHeight > height
    ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }
    onContentYChanged: {
      if (thread.settingY) return
      thread.followBottom = contentY >= contentHeight - height - Style.space(24)
    }
    onContentHeightChanged: {
      var added = contentHeight - thread.lastContentHeight
      thread.lastContentHeight = contentHeight
      if (thread.followBottom) thread.scrollToBottom()
      else if (thread.olderAdded && added > 0) { thread.setY(contentY + added); thread.olderAdded = false }
    }
    onHeightChanged: if (thread.followBottom) thread.scrollToBottom()

    Column {
      id: threadColumn
      width: threadFlick.width
      spacing: Style.space(6)

      Button {
        visible: thread.gm.messagesHasMore && !thread.gm.messagesLoading
        anchors.horizontalCenter: parent.horizontalCenter
        text: "Load older messages"
        foreground: thread.ui.dim
        fontFamily: thread.ui.fontFamily
        fontSize: Style.font.caption
        onClicked: thread.gm.more(thread.gm.activeConversationId)
      }

      Text {
        visible: thread.gm.messagesLoading && thread.gm.messages.length === 0
        anchors.horizontalCenter: parent.horizontalCenter
        text: "Loading…"
        color: thread.ui.dim
        font.family: thread.ui.fontFamily
        font.pixelSize: Style.font.bodySmall
      }

      Text {
        visible: thread.gm.messagesError !== ""
        width: parent.width
        text: thread.gm.messagesError
        color: thread.ui.urgent
        font.family: thread.ui.fontFamily
        font.pixelSize: Style.font.bodySmall
        wrapMode: Text.WordWrap
      }

      Repeater {
        model: thread.gm.messages
        MessageBubble {
          required property var modelData
          required property int index
          ui: thread.ui
          width: threadColumn.width
          msg: modelData
          convId: thread.conv ? thread.conv.id : ""
          fetchable: thread.fetchable
          showDay: index === 0 || !Model.sameDay(new Date(modelData.ts), new Date(thread.gm.messages[index - 1].ts))
          showSender: !!(thread.conv && thread.conv.isGroup && !modelData.fromMe
            && (index === 0 || thread.gm.messages[index - 1].senderId !== modelData.senderId || showDay))
        }
      }

      Text {
        visible: !!(thread.conv && thread.gm.typingIn(thread.conv.id))
        text: "typing…"
        color: thread.ui.dim
        font.family: thread.ui.fontFamily
        font.pixelSize: Style.font.caption
        font.italic: true
      }
    }
  }

  // Files dropped on the thread are staged like picked ones.
  DropArea {
    parent: threadFlick
    anchors.fill: parent
    enabled: thread.canAttach
    keys: ["text/uri-list"]
    onDropped: function(drop) { thread.stageUrls(drop.urls); drop.accept() }
  }

  // Files waiting to go out with the next message
  Flow {
    Layout.fillWidth: true
    visible: thread.staged.length > 0
    spacing: Style.space(6)
    Repeater {
      model: thread.staged
      AttachmentChip {
        required property var modelData
        ui: thread.ui
        path: modelData
        onRemoved: thread.unstage(modelData)
      }
    }
  }

  RowLayout {
    Layout.fillWidth: true
    spacing: Style.space(6)

    PanelActionButton {
      visible: thread.canAttach
      iconText: "󰏢"
      tooltipText: "Attach files (Ctrl+V pastes an image)"
      foreground: thread.ui.dim
      fontFamily: thread.ui.fontFamily
      onClicked: thread.gm.pickFiles(function(paths) { thread.stage(paths); replyField.forceActiveFocus() })
    }

    TextField {
      id: replyField
      Layout.fillWidth: true
      placeholderText: thread.canSend && thread.staged.length > 0 ? "Caption (optional)"
        : thread.canSend ? "Message on " + thread.serviceName
        : thread.serviceName + " is not connected"
      enabled: thread.canSend && !thread.sending
      foreground: thread.ui.foreground
      accent: thread.ui.accent
      onAccepted: thread.sendReply()
      onTextChanged: thread.noteTyping()
      Keys.onEscapePressed: thread.ui.showList()
      // Ctrl+C with nothing selected here copies the highlighted bubble text
      // instead (drag-select already copied on release).
      Keys.onPressed: function(e) {
        if (e.matches(StandardKey.Paste) && thread.canAttach) {
          thread.pasteIntoReply()
          e.accepted = true
          return
        }
        if (e.matches(StandardKey.Copy) && replyField.selectedText === ""
            && thread.ui.selectedBubble && thread.ui.selectedBubble.selectedText !== "") {
          thread.ui.copyToClipboard(thread.ui.selectedBubble.selectedText)
          thread.ui.selectedBubble.flashCopied()
          e.accepted = true
        }
      }
      // Plain assignments: moving with the keys counts as the reader scrolling.
      Keys.onUpPressed: function(e) { threadFlick.contentY = Math.max(0, threadFlick.contentY - Style.space(60)); e.accepted = true }
      Keys.onDownPressed: function(e) { threadFlick.contentY = Math.min(Math.max(0, threadFlick.contentHeight - threadFlick.height), threadFlick.contentY + Style.space(60)); e.accepted = true }
    }

    PanelActionButton {
      iconText: thread.sending ? "󰔟" : "󰒊"
      tooltipText: "Send (Enter)"
      foreground: replyField.text.trim() !== "" || thread.staged.length > 0 ? thread.ui.accent : thread.ui.dim
      fontFamily: thread.ui.fontFamily
      enabled: thread.canSend && !thread.sending && (replyField.text.trim() !== "" || thread.staged.length > 0)
      onClicked: thread.sendReply()
    }
  }
}
