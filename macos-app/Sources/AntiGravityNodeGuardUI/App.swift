import AppKit
import SwiftUI

@MainActor
enum NodeGuardEnvironment {
    static let store: GuardStore = {
        let bundled = Bundle.main.url(forResource: "ag-nodeguard", withExtension: nil)
        let fallback = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/AntiGravityNodeGuard/bin/ag-nodeguard")
        return GuardStore(cliURL: bundled ?? fallback)
    }()
}

@MainActor
final class DashboardWindowController {
    static let shared = DashboardWindowController()
    private var controller: NSWindowController?

    func show(store: GuardStore) {
        NSApp.setActivationPolicy(.accessory)
        NSApp.activate(ignoringOtherApps: true)
        if let window = controller?.window {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }
        let hostingController = NSHostingController(rootView: DashboardView().environment(store))
        let window = NSWindow(contentViewController: hostingController)
        window.title = "Anti Gravity NodeGuard"
        window.setContentSize(NSSize(width: 860, height: 640))
        window.minSize = NSSize(width: 780, height: 560)
        window.styleMask = [.titled, .closable, .miniaturizable, .resizable]
        window.isReleasedWhenClosed = false
        window.center()
        controller = NSWindowController(window: window)
        controller?.showWindow(nil)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }
}

@MainActor
final class NodeGuardAppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        NodeGuardEnvironment.store.start()
        if CommandLine.arguments.contains("--background") {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
                NSApp.windows.forEach { $0.orderOut(nil) }
            }
        } else {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) {
                DashboardWindowController.shared.show(store: NodeGuardEnvironment.store)
            }
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        DashboardWindowController.shared.show(store: NodeGuardEnvironment.store)
        return true
    }
}

struct MenuBarStatusLabel: View {
    @Environment(GuardStore.self) private var store

    var body: some View {
        Image(systemName: store.status.visualState().symbol)
            .symbolRenderingMode(.palette)
            .foregroundStyle(store.status.visualState().color)
    }
}

@main
struct AntiGravityNodeGuardApp: App {
    @NSApplicationDelegateAdaptor(NodeGuardAppDelegate.self) private var appDelegate
    @State private var store = NodeGuardEnvironment.store

    var body: some Scene {
        MenuBarExtra {
            MenuPanelView()
                .environment(store)
        } label: {
            MenuBarStatusLabel()
                .environment(store)
        }
        .menuBarExtraStyle(.window)
    }
}
