import QtQuick
import qs.Commons
import "../Model.js" as Model

// The small colored G/T/W badge that says which service a row or thread is on.
Rectangle {
  id: mark
  property string provider: ""
  property color ringColor: Color.background
  property string fontFamily: Style.font.family
  readonly property var info: Model.providerMark(provider)

  width: Style.space(14)
  height: width
  radius: width / 2
  color: info.color
  border.width: Style.space(2)
  border.color: ringColor

  Text {
    textFormat: Text.PlainText
    anchors.centerIn: parent
    text: mark.info.letter
    color: "#111111"
    font.family: mark.fontFamily
    font.pixelSize: Style.space(7)
    font.bold: true
  }
}
