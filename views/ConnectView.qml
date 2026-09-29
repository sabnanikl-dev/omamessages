import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import qs.Commons
import qs.Ui
import "../Model.js" as Model

// Draws the one step a service is asking for while it connects: a wait, an
// emoji to tap, a QR to scan, a code to type on the phone, or a field to fill
// in. The service moves the step along; this view only shows it and sends
// answers back. Esc (or Cancel) abandons the flow.
Item {
  id: connect
  required property var ui
  required property var gm
  property string providerId: ""
  readonly property var p: gm.provider(providerId)
  readonly property var step: p && p.connect ? p.connect : null
  readonly property string kind: step ? step.kind : ""
  readonly property bool inputFocused: inputField.activeFocus
  readonly property Item inputTarget: inputField
  property bool submitting: false
  property double clock: Date.now()

  // Fresh field for every new input step.
  onStepChanged: {
    if (kind === "input") {
      inputField.text = ""
      Qt.callLater(function() { if (connect.visible && connect.kind === "input") inputField.forceActiveFocus() })
    }
  }

  function submit() {
    var value = inputField.text
    if (value.trim() === "" || submitting) return
    submitting = true
    gm.connectInput(providerId, value, function(ok, err) {
      connect.submitting = false
      if (ok) inputField.text = ""
      else Qt.callLater(function() { inputField.forceActiveFocus() })
    })
  }

  // Ticks the QR "refreshes in" countdown.
  Timer {
    interval: 1000
    repeat: true
    running: connect.visible && connect.kind === "qr"
    triggeredOnStart: true
    onTriggered: connect.clock = Date.now()
  }

  ColumnLayout {
    anchors.fill: parent
    spacing: Style.space(12)

    Item { Layout.fillHeight: true }

    // Emoji to tap on the phone
    Text {
      textFormat: Text.PlainText
      Layout.fillWidth: true
      visible: connect.kind === "emoji"
      text: connect.step ? connect.step.value || "" : ""
      color: connect.ui.foreground
      font.pixelSize: Style.font.displayLarge * 2
      horizontalAlignment: Text.AlignHCenter
    }

    // QR to scan
    Rectangle {
      visible: connect.kind === "qr" && !!connect.step && !!connect.step.qrPath
      Layout.alignment: Qt.AlignHCenter
      width: Style.space(240)
      height: width
      radius: Style.space(8)
      color: "white"
      Image {
        anchors.fill: parent
        anchors.margins: Style.space(10)
        source: connect.kind === "qr" && connect.step.qrPath ? "file://" + connect.step.qrPath + "?t=" + connect.step.qrExpires : ""
        fillMode: Image.PreserveAspectFit
        smooth: false
        cache: false
      }
    }

    // Code to type on the phone
    Text {
      textFormat: Text.PlainText
      Layout.fillWidth: true
      visible: connect.kind === "code"
      text: connect.step ? connect.step.value || "" : ""
      color: connect.ui.foreground
      font.family: connect.ui.fontFamily
      font.pixelSize: Style.font.displayLarge
      font.bold: true
      font.letterSpacing: Style.space(3)
      horizontalAlignment: Text.AlignHCenter
    }

    // The step's own words
    Text {
      textFormat: Text.PlainText
      Layout.fillWidth: true
      visible: text !== ""
      text: {
        if (connect.step) return connect.step.prompt || ""
        if (!connect.p) return "This service isn't running."
        if (connect.p.status === "connected") return "Connected"
        if (connect.p.status === "connecting" || connect.p.status === "pairing") return "Connecting…"
        return connect.p.error || "Not connected"
      }
      color: !connect.step && connect.p && connect.p.status === "disconnected" && connect.p.error ? connect.ui.urgent
           : connect.kind === "input" ? connect.ui.foreground : connect.ui.dim
      font.family: connect.ui.fontFamily
      font.pixelSize: connect.kind === "input" ? Style.font.body : Style.font.bodySmall
      wrapMode: Text.WordWrap
      horizontalAlignment: connect.kind === "input" ? Text.AlignLeft : Text.AlignHCenter
    }

    // Field to fill in
    RowLayout {
      Layout.fillWidth: true
      visible: connect.kind === "input"
      spacing: Style.space(6)

      TextField {
        id: inputField
        Layout.fillWidth: true
        placeholderText: connect.step && connect.step.field === "cookies" ? "Paste cookies or a cURL command"
                       : connect.step && connect.step.field === "phone" ? "+1 555 010 2030"
                       : ""
        password: !!(connect.step && connect.step.secret)
        enabled: !connect.submitting
        foreground: connect.ui.foreground
        accent: connect.ui.accent
        inputMethodHints: connect.step && (connect.step.field === "phone" || connect.step.field === "code") ? Qt.ImhDialableCharactersOnly : Qt.ImhNone
        onAccepted: connect.submit()
        Keys.onEscapePressed: connect.ui.cancelConnect()
      }

      Button {
        text: connect.submitting ? "…" : "Continue"
        foreground: connect.ui.accent
        fontFamily: connect.ui.fontFamily
        bordered: true
        enabled: !connect.submitting && inputField.text.trim() !== ""
        onClicked: connect.submit()
      }
    }

    // Hint, and the QR countdown
    Text {
      textFormat: Text.PlainText
      Layout.fillWidth: true
      visible: text !== ""
      text: {
        var parts = []
        if (connect.step && connect.step.hint) parts.push(connect.step.hint)
        if (connect.kind === "qr" && connect.step.qrExpires) {
          var s = Math.max(0, Math.round((connect.step.qrExpires - connect.clock) / 1000))
          parts.push("Refreshes in " + s + " s")
        }
        return parts.join(" · ")
      }
      color: connect.ui.faint
      font.family: connect.ui.fontFamily
      font.pixelSize: Style.font.caption
      wrapMode: Text.WordWrap
      horizontalAlignment: connect.kind === "input" ? Text.AlignLeft : Text.AlignHCenter
    }

    // Alternatives: "or use a QR code · phone number"
    Flow {
      Layout.fillWidth: true
      visible: !!(connect.step && connect.step.alternatives && connect.step.alternatives.length > 0)
      spacing: Style.space(8)
      Text {
        textFormat: Text.PlainText
        text: "or use"
        color: connect.ui.dim
        font.family: connect.ui.fontFamily
        font.pixelSize: Style.font.caption
      }
      Repeater {
        model: connect.step && connect.step.alternatives ? connect.step.alternatives : []
        Text {
          textFormat: Text.PlainText
          required property var modelData
          text: modelData.label
          color: connect.ui.accent
          font.family: connect.ui.fontFamily
          font.pixelSize: Style.font.caption
          font.underline: altMouse.containsMouse
          MouseArea {
            id: altMouse
            anchors.fill: parent
            hoverEnabled: true
            cursorShape: Qt.PointingHandCursor
            onClicked: connect.gm.connect(connect.providerId, parent.modelData.method)
          }
        }
      }
    }

    // After a failed attempt: try again
    Button {
      Layout.alignment: Qt.AlignHCenter
      visible: !connect.step && !!connect.p && connect.p.status === "disconnected"
      text: "Try again"
      foreground: connect.ui.accent
      fontFamily: connect.ui.fontFamily
      bordered: true
      enabled: !connect.gm.busy
      onClicked: connect.gm.connect(connect.providerId, "")
    }

    Item { Layout.fillHeight: true }

    Button {
      Layout.alignment: Qt.AlignHCenter
      visible: !!connect.step || (connect.p && connect.p.status === "pairing")
      text: "Cancel"
      foreground: connect.ui.foreground
      fontFamily: connect.ui.fontFamily
      onClicked: connect.ui.cancelConnect()
    }
  }
}
