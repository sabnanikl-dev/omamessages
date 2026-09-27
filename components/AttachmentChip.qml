import QtQuick
import qs.Commons
import "../Model.js" as Model

// A file waiting to be sent: a small preview for images, its name, and ✕.
Rectangle {
  id: chip
  required property var ui
  property string path: ""
  signal removed()
  readonly property string name: path.substring(path.lastIndexOf("/") + 1)
  readonly property bool isImage: /\.(png|jpe?g|webp|gif)$/i.test(path)

  implicitWidth: row.implicitWidth + Style.space(14)
  implicitHeight: Style.space(26)
  radius: height / 2
  color: Qt.rgba(ui.foreground.r, ui.foreground.g, ui.foreground.b, 0.09)

  Row {
    id: row
    anchors.verticalCenter: parent.verticalCenter
    anchors.left: parent.left
    anchors.leftMargin: Style.space(6)
    spacing: Style.space(6)

    Rectangle {
      visible: chip.isImage
      width: Style.space(18)
      height: width
      radius: Style.space(3)
      clip: true
      color: "transparent"
      anchors.verticalCenter: parent.verticalCenter
      Image {
        anchors.fill: parent
        source: chip.isImage ? "file://" + chip.path : ""
        sourceSize.width: Style.space(36)
        fillMode: Image.PreserveAspectCrop
        asynchronous: true
      }
    }
    Text {
      visible: !chip.isImage
      text: "󰈔"
      color: chip.ui.dim
      font.family: chip.ui.fontFamily
      font.pixelSize: Style.font.bodySmall
      anchors.verticalCenter: parent.verticalCenter
    }
    Text {
      textFormat: Text.PlainText
      text: chip.name
      width: Math.min(implicitWidth, Style.space(160))
      elide: Text.ElideMiddle
      color: chip.ui.foreground
      font.family: chip.ui.fontFamily
      font.pixelSize: Style.font.caption
      anchors.verticalCenter: parent.verticalCenter
    }
    Text {
      text: "✕"
      color: removeMouse.containsMouse ? chip.ui.urgent : chip.ui.faint
      font.family: chip.ui.fontFamily
      font.pixelSize: Style.font.caption
      anchors.verticalCenter: parent.verticalCenter
      MouseArea {
        id: removeMouse
        anchors.fill: parent
        anchors.margins: -Style.space(4)
        hoverEnabled: true
        cursorShape: Qt.PointingHandCursor
        onClicked: chip.removed()
      }
    }
  }
}
