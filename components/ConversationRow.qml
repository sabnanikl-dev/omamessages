import QtQuick
import QtQuick.Layouts
import qs.Commons
import qs.Ui
import "../Model.js" as Model

// One inbox row. On the All tab it carries a service mark on the avatar.
CursorSurface {
  id: row
  required property var ui
  required property var gm
  property var conv: null
  property int rowIndex: 0
  property bool showMark: false
  readonly property bool unread: conv && conv.unread
  readonly property bool typing: conv && gm.typingIn(conv.id)

  hasCursor: ui.cursorActive && ui.cursorIndex === rowIndex
  foreground: ui.foreground
  implicitHeight: rowContent.implicitHeight + Style.spacing.rowPaddingX

  MouseArea {
    anchors.fill: parent
    hoverEnabled: true
    cursorShape: Qt.PointingHandCursor
    onEntered: { row.ui.cursorActive = true; row.ui.cursorIndex = row.rowIndex }
    onClicked: row.ui.openConversation(row.conv.id)
  }

  RowLayout {
    id: rowContent
    anchors.left: parent.left
    anchors.right: parent.right
    anchors.verticalCenter: parent.verticalCenter
    anchors.leftMargin: Style.space(8)
    anchors.rightMargin: Style.space(10)
    spacing: Style.space(10)

    Rectangle {
      width: Style.space(34)
      height: Style.space(34)
      radius: width / 2
      color: row.unread ? Qt.rgba(row.ui.accent.r, row.ui.accent.g, row.ui.accent.b, 0.28) : Qt.rgba(row.ui.foreground.r, row.ui.foreground.g, row.ui.foreground.b, 0.10)
      Text {
        anchors.centerIn: parent
        text: row.conv && row.conv.isGroup ? "󰡉" : Model.initials(row.conv ? row.conv.name : "")
        color: row.ui.foreground
        font.family: row.ui.fontFamily
        font.pixelSize: row.conv && row.conv.isGroup ? Style.font.icon : Style.font.bodySmall
        font.bold: true
      }
      ServiceMark {
        visible: row.showMark && !!row.conv
        provider: row.conv ? row.conv.provider : ""
        fontFamily: row.ui.fontFamily
        anchors.right: parent.right
        anchors.bottom: parent.bottom
        anchors.rightMargin: -Style.space(3)
        anchors.bottomMargin: -Style.space(3)
      }
    }

    ColumnLayout {
      Layout.fillWidth: true
      spacing: Style.space(2)

      RowLayout {
        Layout.fillWidth: true
        spacing: Style.space(6)
        Text {
          textFormat: Text.PlainText
          Layout.fillWidth: true
          text: row.conv ? row.conv.name : ""
          color: row.ui.foreground
          font.family: row.ui.fontFamily
          font.pixelSize: Style.font.body
          font.bold: row.unread
          elide: Text.ElideRight
        }
        Text {
          textFormat: Text.PlainText
          text: row.conv ? Model.listTime(row.conv.lastTs) : ""
          color: row.unread ? row.ui.accent : row.ui.dim
          font.family: row.ui.fontFamily
          font.pixelSize: Style.font.caption
        }
      }

      RowLayout {
        Layout.fillWidth: true
        visible: row.ui.showPreview
        spacing: Style.space(6)
        Text {
          textFormat: Text.PlainText
          Layout.fillWidth: true
          text: {
            if (!row.conv) return ""
            if (row.typing) return "typing…"
            var prefix = row.conv.lastFromMe ? "You: " : (row.conv.isGroup && row.conv.lastSender ? row.conv.lastSender + ": " : "")
            return prefix + Model.oneLine(row.conv.lastMessage)
          }
          color: row.unread ? row.ui.foreground : row.ui.dim
          font.family: row.ui.fontFamily
          font.pixelSize: Style.font.bodySmall
          font.italic: row.typing
          elide: Text.ElideRight
        }
        Rectangle {
          visible: row.unread
          width: Style.space(8)
          height: width
          radius: width / 2
          color: row.ui.accent
        }
      }
    }
  }
}
