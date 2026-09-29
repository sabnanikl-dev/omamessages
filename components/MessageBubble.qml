import QtQuick
import qs.Commons
import "../Model.js" as Model

// One message: day separator, sender (groups), attachments, text that can be
// drag-selected and copied, and the time/status line.
Item {
  id: bubble
  required property var ui
  property var msg: null
  // The namespaced chat, and whether its service can download attachments.
  property string convId: ""
  property bool fetchable: false
  // Which attachment is being fetched for opening, or -1.
  property int openingIndex: -1
  property bool showDay: false
  property bool showSender: false
  readonly property bool mine: msg && msg.fromMe
  readonly property bool failed: msg && msg.status === "failed"
  readonly property real maxBubbleWidth: Math.round(width * 0.78)
  // Rich text only for messages with links; everything else stays plain.
  readonly property string richBody: msg ? Model.linkify(msg.text, ui.accent) : ""
  // Rich text separates paragraphs with U+2029; copies should have newlines.
  readonly property string selectedText: bodyText.selectedText.replace(/\u2029/g, "\n")

  implicitHeight: bubbleColumn.implicitHeight

  function clearSelection() { bodyText.deselect() }
  function flashCopied() { copiedTimer.restart() }

  // A photo's first click brings the full picture into the chat; the next
  // opens it in the image viewer. Everything else opens on the first click.
  function openAttachment(idx) {
    if (!msg || !fetchable || openingIndex >= 0) return
    var a = msg.attachments[idx]
    openingIndex = idx
    if (a && a.kind === "image" && !a.path)
      ui.service.fetchMedia(convId, msg.id, idx, function() { bubble.openingIndex = -1 })
    else
      ui.service.fetchAndOpen(convId, msg.id, idx, function() { bubble.openingIndex = -1 })
  }

  Timer { id: copiedTimer; interval: 1500 }

  Column {
    id: bubbleColumn
    width: parent.width
    spacing: Style.space(3)

    Text {
      textFormat: Text.PlainText
      visible: bubble.showDay
      anchors.horizontalCenter: parent.horizontalCenter
      topPadding: Style.space(6)
      bottomPadding: Style.space(4)
      text: bubble.msg ? Model.dayLabel(bubble.msg.ts) : ""
      color: bubble.ui.faint
      font.family: bubble.ui.fontFamily
      font.pixelSize: Style.font.caption
    }

    Text {
      textFormat: Text.PlainText
      visible: bubble.showSender
      text: bubble.msg ? (bubble.msg.sender || bubble.msg.senderId) : ""
      color: bubble.ui.dim
      font.family: bubble.ui.fontFamily
      font.pixelSize: Style.font.caption
      leftPadding: Style.space(10)
    }

    Rectangle {
      id: bubbleRect
      anchors.right: bubble.mine ? parent.right : undefined
      anchors.left: bubble.mine ? undefined : parent.left
      width: Math.min(bubble.maxBubbleWidth, Math.max(bodyText.implicitWidth, metaRow.implicitWidth, attachmentColumn.implicitWidth) + Style.space(20))
      height: bubbleInner.implicitHeight + Style.space(14)
      radius: Style.space(12)
      color: bubble.failed ? Qt.rgba(bubble.ui.urgent.r, bubble.ui.urgent.g, bubble.ui.urgent.b, 0.18)
           : bubble.mine ? Qt.rgba(bubble.ui.accent.r, bubble.ui.accent.g, bubble.ui.accent.b, 0.22)
           : Qt.rgba(bubble.ui.foreground.r, bubble.ui.foreground.g, bubble.ui.foreground.b, 0.09)

      // Right-click anywhere on the bubble copies the whole message.
      MouseArea {
        anchors.fill: parent
        acceptedButtons: Qt.RightButton
        onClicked: {
          if (!bubble.msg) return
          bubble.clearSelection()
          bubble.ui.copyToClipboard(bubble.msg.text || "")
          bubble.flashCopied()
        }
      }

      Column {
        id: bubbleInner
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.verticalCenter: parent.verticalCenter
        anchors.leftMargin: Style.space(10)
        anchors.rightMargin: Style.space(10)
        spacing: Style.space(4)

        Column {
          id: attachmentColumn
          visible: !!(bubble.msg && bubble.msg.attachments && bubble.msg.attachments.length > 0)
          spacing: Style.space(4)
          readonly property real maxMediaWidth: Math.min(Style.space(240), bubble.maxBubbleWidth - Style.space(20))
          // A photo downloaded in full gets the bubble's whole width.
          readonly property real maxFullWidth: bubble.maxBubbleWidth - Style.space(20)
          Repeater {
            model: bubble.msg && bubble.msg.attachments ? bubble.msg.attachments : []
            Item {
              id: att
              required property var modelData
              required property int index
              // Photos show the full file once it's downloaded, else the preview.
              readonly property bool full: modelData.kind === "image" && !!modelData.path
              readonly property string picture: full ? modelData.path : (modelData.thumbPath || "")
              readonly property bool hasThumb: picture !== "" && (modelData.kind === "image" || modelData.kind === "video")
              readonly property real mediaWidth: full ? attachmentColumn.maxFullWidth : attachmentColumn.maxMediaWidth
              readonly property bool opening: bubble.openingIndex === index
              readonly property real aspect: modelData.width > 0 && modelData.height > 0 ? modelData.height / modelData.width : 0.66
              // Tall pictures get narrower rather than cropped.
              readonly property real maxHeight: Style.space(full ? 480 : 320)
              readonly property real shownWidth: Math.min(mediaWidth, Math.round(maxHeight / aspect))
              implicitWidth: hasThumb ? shownWidth : fileRow.implicitWidth
              implicitHeight: hasThumb ? Math.min(Math.round(shownWidth * aspect), maxHeight) : fileRow.implicitHeight
              width: implicitWidth
              height: implicitHeight

              // Photo or video preview
              Rectangle {
                visible: att.hasThumb
                anchors.fill: parent
                radius: Style.space(8)
                color: Qt.rgba(bubble.ui.foreground.r, bubble.ui.foreground.g, bubble.ui.foreground.b, 0.08)
                clip: true
                Image {
                  anchors.fill: parent
                  source: att.hasThumb ? "file://" + att.picture : ""
                  // Decode at display size (×2 for HiDPI), not a phone camera's.
                  sourceSize.width: Math.round(att.mediaWidth * 2)
                  fillMode: Image.PreserveAspectCrop
                  asynchronous: true
                  cache: false
                }
                Text {
                  textFormat: Text.PlainText
                  visible: att.modelData.kind === "video"
                  anchors.centerIn: parent
                  text: "󰐊"
                  color: "white"
                  style: Text.Outline
                  styleColor: "#80000000"
                  font.family: bubble.ui.fontFamily
                  font.pixelSize: Style.font.displayLarge
                }
                Text {
                  textFormat: Text.PlainText
                  visible: att.opening
                  anchors.bottom: parent.bottom
                  anchors.left: parent.left
                  anchors.margins: Style.space(6)
                  text: "Downloading…"
                  color: "white"
                  style: Text.Outline
                  styleColor: "#80000000"
                  font.family: bubble.ui.fontFamily
                  font.pixelSize: Style.font.caption
                }
              }

              // File row: icon, name, size
              Row {
                id: fileRow
                visible: !att.hasThumb
                spacing: Style.space(8)
                Text {
                  textFormat: Text.PlainText
                  text: Model.attachmentGlyph(att.modelData.kind)
                  color: bubble.fetchable ? bubble.ui.accent : bubble.ui.foreground
                  font.family: bubble.ui.fontFamily
                  font.pixelSize: Style.font.icon
                  anchors.verticalCenter: parent.verticalCenter
                }
                Column {
                  anchors.verticalCenter: parent.verticalCenter
                  Text {
                    textFormat: Text.PlainText
                    text: att.modelData.name || att.modelData.kind
                    color: bubble.ui.foreground
                    font.family: bubble.ui.fontFamily
                    font.pixelSize: Style.font.bodySmall
                    width: Math.min(implicitWidth, attachmentColumn.maxMediaWidth - Style.space(30))
                    elide: Text.ElideMiddle
                  }
                  Text {
                    textFormat: Text.PlainText
                    visible: text !== ""
                    text: {
                      var parts = []
                      if (att.modelData.size > 0) parts.push(Model.formatSize(att.modelData.size))
                      if (att.opening) parts.push("downloading…")
                      else if (bubble.fetchable) parts.push("click to open")
                      return parts.join(" · ")
                    }
                    color: bubble.ui.dim
                    font.family: bubble.ui.fontFamily
                    font.pixelSize: Style.font.caption
                  }
                }
              }

              MouseArea {
                anchors.fill: parent
                enabled: bubble.fetchable && !att.opening
                cursorShape: bubble.fetchable ? Qt.PointingHandCursor : Qt.ArrowCursor
                onClicked: bubble.openAttachment(att.index)
              }
            }
          }
        }

        // Read-only TextEdit so a drag can highlight part of the message.
        // Selection is driven by the MouseArea below (not selectByMouse) so
        // the Flickable never steals the drag and focus stays in the reply box.
        TextEdit {
          id: bodyText
          visible: text !== ""
          width: Math.min(implicitWidth, bubble.maxBubbleWidth - Style.space(20))
          text: bubble.richBody !== "" ? bubble.richBody : (bubble.msg ? bubble.msg.text : "")
          color: bubble.ui.foreground
          font.family: bubble.ui.fontFamily
          font.pixelSize: Style.font.body
          wrapMode: TextEdit.Wrap
          textFormat: bubble.richBody !== "" ? TextEdit.RichText : TextEdit.PlainText
          readOnly: true
          selectByMouse: false
          activeFocusOnPress: false
          cursorVisible: false
          persistentSelection: true
          selectionColor: Qt.rgba(bubble.ui.accent.r, bubble.ui.accent.g, bubble.ui.accent.b, 0.45)
          selectedTextColor: bubble.ui.foreground

          MouseArea {
            id: selectArea
            anchors.fill: parent
            acceptedButtons: Qt.LeftButton
            cursorShape: Qt.IBeamCursor
            preventStealing: true
            hoverEnabled: bubble.richBody !== ""
            property int anchorPos: -1

            onPressed: function(e) {
              bubble.ui.clearBubbleSelection()
              bubble.ui.selectedBubble = bubble
              anchorPos = bodyText.positionAt(e.x, e.y)
              bodyText.cursorPosition = anchorPos
            }
            onPositionChanged: function(e) {
              if (!pressed) {
                // A hand over links, the text cursor elsewhere.
                cursorShape = bodyText.linkAt(e.x, e.y) !== "" ? Qt.PointingHandCursor : Qt.IBeamCursor
                return
              }
              if (anchorPos < 0) return
              var y = Math.max(0, Math.min(e.y, bodyText.height - 1))
              bodyText.select(anchorPos, bodyText.positionAt(e.x, y))
            }
            onReleased: function(e) {
              anchorPos = -1
              if (bodyText.selectedText !== "") {
                bubble.ui.copyToClipboard(bubble.selectedText)
                bubble.flashCopied()
                return
              }
              // A click, not a drag: open the link under the pointer.
              var link = bodyText.linkAt(e.x, e.y)
              if (link !== "") bubble.ui.service.openLink(link)
            }
            onDoubleClicked: function(e) {
              bodyText.cursorPosition = bodyText.positionAt(e.x, e.y)
              bodyText.selectWord()
              if (bodyText.selectedText !== "") {
                bubble.ui.copyToClipboard(bubble.selectedText)
                bubble.flashCopied()
              }
            }
          }
        }

        Row {
          id: metaRow
          anchors.right: parent.right
          spacing: Style.space(4)
          Text {
            textFormat: Text.PlainText
            visible: copiedTimer.running
            text: "copied"
            color: bubble.ui.accent
            font.family: bubble.ui.fontFamily
            font.pixelSize: Style.font.caption
          }
          Text {
            textFormat: Text.PlainText
            visible: !!(bubble.msg && bubble.msg.reactions && bubble.msg.reactions.length > 0)
            text: bubble.msg && bubble.msg.reactions ? bubble.msg.reactions.join(" ") : ""
            font.pixelSize: Style.font.caption
          }
          Text {
            textFormat: Text.PlainText
            text: bubble.msg ? Model.clock(bubble.msg.ts) : ""
            color: bubble.ui.faint
            font.family: bubble.ui.fontFamily
            font.pixelSize: Style.font.caption
          }
          Text {
            textFormat: Text.PlainText
            visible: bubble.mine
            text: bubble.msg ? Model.statusGlyph(bubble.msg.status) : ""
            color: bubble.failed ? bubble.ui.urgent : (bubble.msg && bubble.msg.status === "read" ? bubble.ui.accent : bubble.ui.faint)
            font.family: bubble.ui.fontFamily
            font.pixelSize: Style.font.caption
          }
        }
      }
    }

    Text {
      textFormat: Text.PlainText
      visible: !!(bubble.failed && bubble.msg && bubble.msg.statusText)
      anchors.right: parent.right
      text: (bubble.msg && bubble.msg.statusText) || ""
      color: bubble.ui.urgent
      font.family: bubble.ui.fontFamily
      font.pixelSize: Style.font.caption
      rightPadding: Style.space(4)
    }
  }
}
