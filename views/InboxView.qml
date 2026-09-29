import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import qs.Commons
import qs.Ui
import "../components"
import "../Model.js" as Model

// The tab strip and the conversation list for the current tab. All merges
// every service and marks each row with its service; the other tabs filter.
ColumnLayout {
  id: inbox
  required property var ui
  required property var gm
  property string tab: "all"
  readonly property var model: Model.filterByTab(gm.conversations, tab)
  readonly property var tabProvider: tab === "all" ? null : gm.provider(tab)
  readonly property real listHeight: listColumn.implicitHeight

  spacing: Style.space(6)

  function scrollTo(index) {
    if (index < 0 || index >= listColumn.children.length) return
    var item = listColumn.children[index]
    Qt.callLater(function() {
      if (!item) return
      var top = item.mapToItem(listFlick.contentItem, 0, 0).y
      var bottom = top + item.height
      var maxY = Math.max(0, listFlick.contentHeight - listFlick.height)
      if (top < listFlick.contentY) listFlick.contentY = Math.max(0, top - Style.space(6))
      else if (bottom > listFlick.contentY + listFlick.height) listFlick.contentY = Math.min(maxY, bottom + Style.space(6) - listFlick.height)
    })
  }

  function unreadFor(tabId) {
    if (tabId === "all") return gm.unreadCount
    var p = gm.provider(tabId)
    return p ? Number(p.unread || 0) : 0
  }

  // Tabs ---------------------------------------------------------------------
  RowLayout {
    Layout.fillWidth: true
    spacing: 0

    Repeater {
      model: inbox.ui.tabs
      Item {
        required property var modelData
        readonly property bool current: inbox.tab === modelData.id
        readonly property int unread: inbox.unreadFor(modelData.id)
        Layout.preferredWidth: tabLabel.implicitWidth + Style.space(18)
        Layout.preferredHeight: tabLabel.implicitHeight + Style.space(12)

        Text {
          id: tabLabel
          anchors.centerIn: parent
          textFormat: Text.StyledText
          text: parent.modelData.label + (parent.unread > 0
            ? " <font color='" + inbox.ui.urgent + "'><small>" + parent.unread + "</small></font>" : "")
          color: parent.current ? inbox.ui.foreground : inbox.ui.dim
          font.family: inbox.ui.fontFamily
          font.pixelSize: Style.font.bodySmall
          font.bold: parent.current
        }

        Rectangle {
          anchors.left: parent.left
          anchors.right: parent.right
          anchors.bottom: parent.bottom
          height: Style.space(2)
          color: parent.current ? inbox.ui.accent : "transparent"
        }

        MouseArea {
          anchors.fill: parent
          cursorShape: Qt.PointingHandCursor
          onClicked: inbox.ui.setTab(parent.modelData.id)
        }
      }
    }

    Item { Layout.fillWidth: true }
  }

  // List ---------------------------------------------------------------------
  Flickable {
    id: listFlick
    Layout.fillWidth: true
    Layout.fillHeight: true
    contentWidth: width
    contentHeight: listColumn.implicitHeight
    clip: true
    boundsBehavior: Flickable.StopAtBounds
    flickableDirection: Flickable.VerticalFlick
    interactive: contentHeight > height
    ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

    Column {
      id: listColumn
      width: listFlick.width
      spacing: Style.space(4)

      Repeater {
        model: inbox.model
        ConversationRow {
          required property var modelData
          required property int index
          width: listColumn.width
          ui: inbox.ui
          gm: inbox.gm
          conv: modelData
          rowIndex: index
          showMark: inbox.tab === "all"
        }
      }
    }

    Text {
      textFormat: Text.PlainText
      visible: inbox.model.length === 0
      anchors.centerIn: parent
      width: parent.width - Style.space(40)
      horizontalAlignment: Text.AlignHCenter
      wrapMode: Text.WordWrap
      text: {
        if (inbox.tab === "all") return inbox.gm.anyEnabled ? "No conversations yet." : "No services are turned on."
        var p = inbox.tabProvider
        var name = inbox.gm.providerName(inbox.tab)
        if (!p || !p.enabled) return name + " isn't set up yet."
        if (p.status !== "connected") return name + ": " + Model.statusLine(p.status, p.error, p.account, p.lastActivity, inbox.ui.now)
        return "No " + name + " conversations yet."
      }
      color: inbox.ui.dim
      font.family: inbox.ui.fontFamily
      font.pixelSize: Style.font.body
    }
  }
}
