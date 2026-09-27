import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import qs.Commons
import qs.Ui
import "../components"
import "../Model.js" as Model

// One row per service: its status, the account it's signed in as, and the
// one thing you can do about it (Connect, Continue, Disconnect). Disconnect
// asks first, in the row.
ColumnLayout {
  id: accounts
  required property var ui
  required property var gm
  // Which row, if any, is asking "Disconnect?"
  property string confirming: ""

  // Real services first, then anything else the daemon runs (the fake), then
  // the one we know is coming.
  readonly property var rows: {
    var out = []
    for (var i = 0; i < Model.services.length; i++) {
      var s = Model.services[i]
      out.push({ id: s.id, name: s.full, p: gm.provider(s.id) })
    }
    for (var j = 0; j < gm.providers.length; j++) {
      var p = gm.providers[j]
      if (!Model.services.some(function(s) { return s.id === p.id })) out.push({ id: p.id, name: p.name, p: p })
    }
    out.push({ id: "signal", name: "Signal", p: null, later: true })
    return out
  }

  spacing: Style.space(4)

  // The row's action: "connect" | "continue" | "disconnect" | "".
  function actionFor(row) {
    var p = row.p
    if (!p || !p.enabled || (p.extra && p.extra.preview)) return ""
    if (p.status === "pairing") return "continue"
    if (p.status === "disconnected") return "connect"
    return "disconnect"
  }

  function activate(index) {
    if (index < 0 || index >= rows.length) return
    var row = rows[index]
    switch (actionFor(row)) {
      case "connect": ui.startConnect(row.id, ""); break
      case "continue": ui.showConnect(row.id); break
      case "disconnect":
        if (confirming === row.id) { confirming = ""; gm.disconnect(row.id) }
        else confirming = row.id
        break
    }
  }

  function detail(row) {
    var p = row.p
    if (row.later) return "Coming later"
    if (!p) return "Not in this build yet"
    if (!p.enabled) return "Turned off in settings"
    if (p.extra && p.extra.preview) return "Read-only preview until cutover" + (p.account ? " · " + p.account : "")
    switch (p.status) {
      case "connected": {
        var parts = ["Connected"]
        if (p.extra && p.extra.phone) parts.push(p.extra.phone)
        if (p.account) parts.push(p.account)
        return parts.join(" · ")
      }
      case "pairing": return "Connecting… (not finished)"
      case "connecting": return "Connecting…"
      case "phone_offline": return p.error || "Phone is not responding"
      case "error": return p.error || "Something went wrong"
    }
    return p.error || "Not connected"
  }

  Repeater {
    model: accounts.rows

    CursorSurface {
      id: row
      required property var modelData
      required property int index
      readonly property string action: accounts.actionFor(modelData)
      readonly property string tone: modelData.p ? Model.statusTone(modelData.p.status) : "off"
      readonly property bool askingToDisconnect: accounts.confirming === modelData.id

      Layout.fillWidth: true
      hasCursor: accounts.ui.cursorActive && accounts.ui.cursorIndex === index
      foreground: accounts.ui.foreground
      implicitHeight: rowContent.implicitHeight + Style.spacing.rowPaddingX

      MouseArea {
        anchors.fill: parent
        hoverEnabled: true
        onEntered: { accounts.ui.cursorActive = true; accounts.ui.cursorIndex = row.index }
      }

      RowLayout {
        id: rowContent
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.verticalCenter: parent.verticalCenter
        anchors.leftMargin: Style.space(8)
        anchors.rightMargin: Style.space(8)
        spacing: Style.space(10)

        ServiceMark {
          provider: row.modelData.id
          fontFamily: accounts.ui.fontFamily
          width: Style.space(22)
          opacity: row.modelData.later || !row.modelData.p ? 0.45 : 1
        }

        ColumnLayout {
          Layout.fillWidth: true
          spacing: Style.space(2)

          Text {
            textFormat: Text.PlainText
            Layout.fillWidth: true
            text: row.askingToDisconnect ? "Disconnect " + row.modelData.name + "?" : row.modelData.name
            color: row.modelData.later || !row.modelData.p ? accounts.ui.dim : accounts.ui.foreground
            font.family: accounts.ui.fontFamily
            font.pixelSize: Style.font.body
            font.bold: true
            elide: Text.ElideRight
          }

          RowLayout {
            Layout.fillWidth: true
            spacing: Style.space(5)
            Rectangle {
              visible: !!row.modelData.p && !row.askingToDisconnect
              width: Style.space(7)
              height: width
              radius: width / 2
              color: row.tone === "ok" ? "#7fbf7b" : row.tone === "busy" ? accounts.ui.accent : row.tone === "trouble" ? accounts.ui.urgent : accounts.ui.faint
            }
            Text {
              textFormat: Text.PlainText
              Layout.fillWidth: true
              text: row.askingToDisconnect ? "Its chats are removed from this computer." : accounts.detail(row.modelData)
              color: row.tone === "trouble" && !row.askingToDisconnect ? accounts.ui.urgent : accounts.ui.dim
              font.family: accounts.ui.fontFamily
              font.pixelSize: Style.font.caption
              wrapMode: Text.WordWrap
              maximumLineCount: 2
              elide: Text.ElideRight
            }
          }
        }

        Button {
          visible: row.askingToDisconnect
          text: "Keep"
          foreground: accounts.ui.foreground
          fontFamily: accounts.ui.fontFamily
          onClicked: accounts.confirming = ""
        }

        Button {
          visible: row.action !== ""
          text: row.askingToDisconnect ? "Disconnect"
              : row.action === "connect" ? (row.modelData.p && row.modelData.p.error ? "Reconnect" : "Connect")
              : row.action === "continue" ? "Continue" : "Disconnect"
          foreground: row.action === "disconnect" ? (row.askingToDisconnect ? accounts.ui.urgent : accounts.ui.foreground) : accounts.ui.accent
          fontFamily: accounts.ui.fontFamily
          bordered: row.action !== "disconnect" || row.askingToDisconnect
          enabled: !accounts.gm.busy || row.action === "continue"
          onClicked: accounts.activate(row.index)
        }
      }
    }
  }

  Item { Layout.fillHeight: true }

  Text {
    Layout.fillWidth: true
    text: "Disconnecting removes that service's chats from this computer only."
    color: accounts.ui.faint
    font.family: accounts.ui.fontFamily
    font.pixelSize: Style.font.caption
    wrapMode: Text.WordWrap
    horizontalAlignment: Text.AlignHCenter
  }
}
