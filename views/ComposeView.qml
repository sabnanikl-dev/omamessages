import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import qs.Commons
import qs.Ui
import "../components"
import "../Model.js" as Model

// New message: pick the service first, then the person, then say something.
// The recipient field searches the service's contacts as you type; anything
// else typed there (a number, a @username) is sent to the service as is.
ColumnLayout {
  id: compose
  required property var ui
  required property var gm
  property string providerId: ""
  // The contact picked from the results, or null to use the typed text.
  property var picked: null
  property var results: []
  property int highlight: -1
  property bool sending: false
  readonly property bool inputFocused: toField.activeFocus || textField.activeFocus
  readonly property Item recipientInput: toField

  // Services that are connected and can start chats.
  readonly property var services: gm.providers.filter(function(p) {
    return p.enabled && p.status === "connected" && p.caps && (p.caps.startByNumber || p.caps.contactSearch)
  })
  readonly property var service: gm.provider(providerId)
  readonly property bool canSearch: !!(service && service.caps && service.caps.contactSearch)

  spacing: Style.space(10)

  // Called when the view opens: prefer the tab's service.
  function reset(preferred) {
    picked = null
    results = []
    highlight = -1
    toField.text = ""
    textField.text = ""
    var ids = services.map(function(p) { return p.id })
    providerId = ids.indexOf(preferred) >= 0 ? preferred : (ids.indexOf(providerId) >= 0 ? providerId : (ids[0] || ""))
  }

  function placeholderFor(id) {
    switch (id) {
      case "telegram": return "Name, @username, or phone number"
      case "gmessages": return "Name or phone number"
      case "whatsapp": return "Name or phone number"
    }
    return "Recipient"
  }

  function search() {
    if (!canSearch || picked) return
    var q = toField.text.trim()
    if (q.length < 2) { results = []; highlight = -1; return }
    var asked = providerId + "\n" + q
    gm.contacts(providerId, q, function(list) {
      if (providerId + "\n" + toField.text.trim() !== asked) return // typed on
      results = list.slice(0, 8)
      highlight = results.length > 0 ? 0 : -1
    })
  }

  function pick(i) {
    if (i < 0 || i >= results.length) return
    picked = results[i]
    results = []
    highlight = -1
    textField.forceActiveFocus()
  }

  function send() {
    var to = picked ? picked.id : toField.text.trim()
    var text = textField.text.trim()
    if (!providerId || !to || sending) return
    sending = true
    gm.newChat(providerId, to, text, function(ok, id, err) {
      compose.sending = false
      if (id) ui.openConversation(id)
    })
  }

  Timer {
    id: searchDelay
    interval: 300
    onTriggered: compose.search()
  }

  // Nothing to send with
  Text {
    Layout.fillWidth: true
    visible: compose.services.length === 0
    text: "Connect a service first."
    color: compose.ui.dim
    font.family: compose.ui.fontFamily
    font.pixelSize: Style.font.body
    horizontalAlignment: Text.AlignHCenter
    topPadding: Style.space(40)
  }

  Button {
    Layout.alignment: Qt.AlignHCenter
    visible: compose.services.length === 0
    text: "Accounts"
    foreground: compose.ui.accent
    fontFamily: compose.ui.fontFamily
    bordered: true
    onClicked: compose.ui.showAccounts()
  }

  // Service picker
  Flow {
    Layout.fillWidth: true
    visible: compose.services.length > 0
    spacing: Style.space(6)

    Repeater {
      model: compose.services
      Rectangle {
        required property var modelData
        readonly property bool current: compose.providerId === modelData.id
        width: chipRow.implicitWidth + Style.space(16)
        height: chipRow.implicitHeight + Style.space(10)
        radius: height / 2
        color: current ? Qt.rgba(compose.ui.accent.r, compose.ui.accent.g, compose.ui.accent.b, 0.22) : "transparent"
        border.width: 1
        border.color: current ? compose.ui.accent : compose.ui.faint

        Row {
          id: chipRow
          anchors.centerIn: parent
          spacing: Style.space(6)
          ServiceMark {
            provider: parent.parent.modelData.id
            fontFamily: compose.ui.fontFamily
            ringColor: "transparent"
            anchors.verticalCenter: parent.verticalCenter
          }
          Text {
            text: Model.serviceName(parent.parent.modelData.id, parent.parent.modelData.name)
            color: parent.parent.current ? compose.ui.foreground : compose.ui.dim
            font.family: compose.ui.fontFamily
            font.pixelSize: Style.font.bodySmall
            anchors.verticalCenter: parent.verticalCenter
          }
        }

        MouseArea {
          anchors.fill: parent
          cursorShape: Qt.PointingHandCursor
          onClicked: {
            compose.providerId = parent.modelData.id
            compose.picked = null
            compose.results = []
            toField.forceActiveFocus()
          }
        }
      }
    }
  }

  // Recipient: a picked contact shows as a chip; otherwise a search field.
  RowLayout {
    Layout.fillWidth: true
    visible: compose.services.length > 0 && !!compose.picked
    spacing: Style.space(8)
    Text {
      Layout.fillWidth: true
      textFormat: Text.PlainText
      text: compose.picked ? compose.picked.name + (compose.picked.number ? "  " + compose.picked.number : "") : ""
      color: compose.ui.foreground
      font.family: compose.ui.fontFamily
      font.pixelSize: Style.font.body
      elide: Text.ElideRight
    }
    Button {
      text: "Change"
      foreground: compose.ui.dim
      fontFamily: compose.ui.fontFamily
      onClicked: { compose.picked = null; toField.forceActiveFocus() }
    }
  }

  TextField {
    id: toField
    Layout.fillWidth: true
    visible: compose.services.length > 0 && !compose.picked
    placeholderText: compose.placeholderFor(compose.providerId)
    foreground: compose.ui.foreground
    accent: compose.ui.accent
    onTextChanged: { compose.results = []; compose.highlight = -1; searchDelay.restart() }
    onAccepted: {
      if (compose.highlight >= 0) compose.pick(compose.highlight)
      else textField.forceActiveFocus()
    }
    Keys.onDownPressed: function(e) { if (compose.results.length) compose.highlight = Math.min(compose.results.length - 1, compose.highlight + 1); e.accepted = true }
    Keys.onUpPressed: function(e) { if (compose.results.length) compose.highlight = Math.max(0, compose.highlight - 1); e.accepted = true }
    Keys.onEscapePressed: compose.ui.showList()
    Keys.onTabPressed: textField.forceActiveFocus()
  }

  // Search results
  Column {
    Layout.fillWidth: true
    visible: compose.results.length > 0 && !compose.picked
    spacing: Style.space(2)
    Repeater {
      model: compose.results
      CursorSurface {
        required property var modelData
        required property int index
        width: parent.width
        hasCursor: compose.highlight === index
        foreground: compose.ui.foreground
        implicitHeight: resultRow.implicitHeight + Style.space(10)
        RowLayout {
          id: resultRow
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.verticalCenter: parent.verticalCenter
          anchors.leftMargin: Style.space(8)
          anchors.rightMargin: Style.space(8)
          Text {
            Layout.fillWidth: true
            textFormat: Text.PlainText
            text: parent.parent.modelData.name
            color: compose.ui.foreground
            font.family: compose.ui.fontFamily
            font.pixelSize: Style.font.bodySmall
            elide: Text.ElideRight
          }
          Text {
            textFormat: Text.PlainText
            text: parent.parent.modelData.number || ""
            color: compose.ui.dim
            font.family: compose.ui.fontFamily
            font.pixelSize: Style.font.caption
          }
        }
        MouseArea {
          anchors.fill: parent
          hoverEnabled: true
          cursorShape: Qt.PointingHandCursor
          onEntered: compose.highlight = parent.index
          onClicked: compose.pick(parent.index)
        }
      }
    }
  }

  TextField {
    id: textField
    Layout.fillWidth: true
    visible: compose.services.length > 0
    placeholderText: "Message on " + Model.serviceName(compose.providerId, compose.providerId)
    enabled: !compose.sending
    foreground: compose.ui.foreground
    accent: compose.ui.accent
    onAccepted: compose.send()
    Keys.onEscapePressed: compose.ui.showList()
  }

  RowLayout {
    Layout.fillWidth: true
    visible: compose.services.length > 0
    Item { Layout.fillWidth: true }
    Button {
      text: "Cancel"
      foreground: compose.ui.foreground
      fontFamily: compose.ui.fontFamily
      onClicked: compose.ui.showList()
    }
    Button {
      text: compose.sending ? "…" : (textField.text.trim() ? "Send" : "Open chat")
      foreground: compose.ui.accent
      fontFamily: compose.ui.fontFamily
      bordered: true
      enabled: !compose.sending && (!!compose.picked || toField.text.trim() !== "")
      onClicked: compose.send()
    }
  }

  Item { Layout.fillHeight: true }
}
