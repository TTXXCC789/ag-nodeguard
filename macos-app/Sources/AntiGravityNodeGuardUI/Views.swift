import AppKit
import SwiftUI

struct StatusPill: View {
    let state: GuardVisualState

    var body: some View {
        Label(state.title, systemImage: state.symbol)
            .font(.caption.weight(.semibold))
            .foregroundStyle(state.color)
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .background(state.color.opacity(0.12), in: Capsule())
    }
}

struct MetricCard: View {
    let title: String
    let value: String
    let detail: String
    let symbol: String
    let color: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Image(systemName: symbol)
                    .foregroundStyle(color)
                Text(title)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
            }
            Text(value)
                .font(.title3.weight(.semibold))
                .lineLimit(1)
            Text(detail)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }
}

struct AutoSwitchToggle: View {
    @Environment(GuardStore.self) private var store

    var body: some View {
        Toggle(
            isOn: Binding(
                get: { store.status.autoSwitchEnabled },
                set: { enabled in Task { await store.setAutoSwitch(enabled) } }
            )
        ) {
            VStack(alignment: .leading, spacing: 2) {
                Text("自动切换")
                    .font(.headline)
                Text(store.status.paused ? "监控保留，切换和 Retry 已暂停" : "网络故障时自动选择绿色节点")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .toggleStyle(.switch)
        .disabled(store.isActionRunning || !store.status.installed)
    }
}

struct MenuPanelView: View {
    @Environment(GuardStore.self) private var store

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 12) {
                ZStack {
                    RoundedRectangle(cornerRadius: 12)
                        .fill(store.status.visualState().color.gradient)
                    Image(systemName: "point.3.connected.trianglepath.dotted")
                        .font(.title2)
                        .foregroundStyle(.white)
                }
                .frame(width: 44, height: 44)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Anti Gravity NodeGuard")
                        .font(.headline)
                    StatusPill(state: store.status.visualState())
                }
                Spacer()
            }

            if !store.status.installed {
                VStack(alignment: .leading, spacing: 10) {
                    Text("后台服务尚未安装")
                        .font(.headline)
                    Text("安装会创建 Anti Gravity 专用策略组，不改变其他应用当前使用的节点。")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Button("安装后台服务") { Task { await store.installService() } }
                        .buttonStyle(.borderedProminent)
                        .disabled(store.isActionRunning)
                }
                .padding(14)
                .background(.orange.opacity(0.1), in: RoundedRectangle(cornerRadius: 12))
            } else {
                if store.componentUpdateAvailable {
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("后台组件有更新")
                                .font(.headline)
                            Text("更新会保留现有配置和当前专用节点。")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                        Button("更新") { Task { await store.installService() } }
                            .disabled(store.isActionRunning)
                    }
                    .padding(12)
                    .background(.blue.opacity(0.1), in: RoundedRectangle(cornerRadius: 10))
                }
                AutoSwitchToggle()
                Divider()
                LabeledContent("首选套餐", value: store.status.preferredSubscriptionName ?? "未选择")
                LabeledContent("实际套餐", value: store.status.currentSubscriptionName ?? "—")
                if store.status.usingFallback == true {
                    Label("首选套餐暂无可用节点，当前正在跨套餐兜底", systemImage: "arrow.triangle.branch")
                        .font(.caption)
                        .foregroundStyle(.orange)
                }
                LabeledContent("当前节点", value: store.status.currentNode ?? "未知")
                LabeledContent("节点延迟", value: store.status.currentDelayMS.map { "\($0) ms" } ?? "—")
                LabeledContent("出口地区", value: store.status.exitRegion ?? "—")
                LabeledContent("Anti Gravity", value: store.status.antigravityOK ? "Agent 连接正常" : "Agent 连接异常")
                LabeledContent("本地服务", value: (store.status.localHealthOK ?? false) ? "运行中" : "未运行")
                LabeledContent("状态来源", value: "后台 language_server.log")
                if store.status.isStale() {
                    Label("状态数据已超过 6 秒未更新", systemImage: "clock.badge.exclamationmark")
                        .font(.caption)
                        .foregroundStyle(.orange)
                }
                if let error = store.status.lastError, !error.isEmpty {
                    Text(error)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }

            Divider()
            HStack {
                Button("打开仪表盘") {
                    DashboardWindowController.shared.show(store: store)
                }
                .buttonStyle(.borderedProminent)
                Spacer()
                Button {
                    Task { await store.refreshCandidates() }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .help("刷新候选节点")
                .disabled(store.isRefreshingCandidates)
                Button("立即恢复") {
                    Task { await store.recoverNow() }
                }
                .help("切换绿色节点，并在后台点击 Retry")
                .disabled(store.isActionRunning || store.status.paused || !store.status.daemonRunning)
                Button("退出界面") {
                    store.shutdownUI()
                    NSApp.terminate(nil)
                }
            }
        }
        .padding(18)
        .frame(width: 370)
        .alert("操作失败", isPresented: errorBinding) {
            Button("好", role: .cancel) { store.errorMessage = nil }
        } message: {
            Text(store.errorMessage ?? "未知错误")
        }
    }

    private var errorBinding: Binding<Bool> {
        Binding(get: { store.errorMessage != nil }, set: { if !$0 { store.errorMessage = nil } })
    }
}

struct DashboardView: View {
    @Environment(GuardStore.self) private var store

    var body: some View {
        @Bindable var store = store
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                header
                subscriptionSection
                LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 14) {
                    MetricCard(
                        title: "当前节点",
                        value: store.status.currentNode ?? "未知",
                        detail: [store.status.currentSubscriptionName, store.status.currentDelayMS.map { "\($0) ms" }].compactMap { $0 }.joined(separator: " · "),
                        symbol: "point.3.connected.trianglepath.dotted",
                        color: .blue
                    )
                    MetricCard(
                        title: "出口网络",
                        value: store.status.exitRegion ?? "未知",
                        detail: store.status.exitIP ?? "尚未记录出口 IP",
                        symbol: "globe.asia.australia.fill",
                        color: .purple
                    )
                    MetricCard(
                        title: "Anti Gravity",
                        value: store.status.antigravityOK ? "连接正常" : "连接异常",
                        detail: "本地服务 \((store.status.localHealthOK ?? false) ? "正常" : "异常") · 检查于 \(store.status.lastHealthCheck?.nodeGuardDisplay ?? "—")",
                        symbol: store.status.antigravityOK ? "checkmark.circle.fill" : "exclamationmark.circle.fill",
                        color: store.status.antigravityOK ? .green : .orange
                    )
                }

                HStack(alignment: .top, spacing: 16) {
                    candidatesSection
                    activitySection
                        .frame(width: 275)
                }
                controls
            }
            .padding(24)
        }
        .frame(minWidth: 900, minHeight: 620)
        .background(Color(nsColor: .windowBackgroundColor))
        .alert("停止后台服务？", isPresented: $store.confirmingStop) {
            Button("取消", role: .cancel) {}
            Button("停止", role: .destructive) { Task { await store.stopService() } }
        } message: {
            Text("停止后将不再监控或自动切换。退出桌面界面不会停止后台服务。")
        }
        .alert("操作失败", isPresented: errorBinding) {
            Button("好", role: .cancel) { store.errorMessage = nil }
        } message: {
            Text(store.errorMessage ?? "未知错误")
        }
        .sheet(isPresented: $store.showingDoctor) {
            VStack(alignment: .leading, spacing: 14) {
                Text("环境诊断")
                    .font(.title2.weight(.semibold))
                ScrollView {
                    Text(store.doctorOutput)
                        .font(.system(.body, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                HStack {
                    Spacer()
                    Button("关闭") { store.showingDoctor = false }
                        .keyboardShortcut(.defaultAction)
                }
            }
            .padding(20)
            .frame(width: 680, height: 470)
        }
    }

    private var subscriptionSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Text("订阅套餐")
                        .font(.title3.weight(.semibold))
                    Text("只改变 Anti Gravity 的专用节点池，不切换 Clash Verge 当前订阅。")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if store.isSelectingSubscription || store.isRefreshingSubscriptions {
                    ProgressView().controlSize(.small)
                }
                Button("刷新订阅") { Task { await store.refreshSubscriptions(force: true) } }
                    .disabled(store.isSelectingSubscription || store.isRefreshingSubscriptions)
            }
            Picker(
                "首选套餐",
                selection: Binding(
                    get: { store.status.preferredSubscriptionUID ?? "" },
                    set: { uid in Task { await store.selectSubscription(uid) } }
                )
            ) {
                ForEach(store.subscriptions) { subscription in
                    Text(subscriptionLabel(subscription))
                        .tag(subscription.uid)
                        .disabled(!subscription.supported)
                }
            }
            .pickerStyle(.menu)
            .disabled(store.isSelectingSubscription || store.isRefreshingSubscriptions || !store.status.daemonRunning)
            if store.status.usingFallback == true {
                Label("当前使用 \(store.status.currentSubscriptionName ?? "其他套餐") 兜底；首选套餐恢复后将在安全时机回切。", systemImage: "arrow.triangle.branch")
                    .font(.caption)
                    .foregroundStyle(.orange)
            } else if let current = store.status.currentSubscriptionName {
                Label("当前节点来自 \(current)", systemImage: "checkmark.circle.fill")
                    .font(.caption)
                    .foregroundStyle(.green)
            }
            ForEach(store.subscriptions.filter { !$0.supported }) { subscription in
                Text("\(subscription.name)：\(subscription.unsupportedReason ?? "暂不支持")")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    private func subscriptionLabel(_ subscription: Subscription) -> String {
        let active = subscription.clashActive ? " · Clash 当前" : ""
        let support = subscription.supported ? "\(subscription.greenNodeCount)/\(subscription.nodeCount) 绿色" : "不支持"
        return "\(subscription.name) · \(support)\(active)"
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 16) {
            VStack(alignment: .leading, spacing: 5) {
                Text("Anti Gravity NodeGuard")
                    .font(.largeTitle.weight(.bold))
                HStack(spacing: 10) {
                    StatusPill(state: store.status.visualState())
                    if store.status.isStale() {
                        Text("数据已过期")
                            .font(.caption)
                            .foregroundStyle(.orange)
                    }
                }
            }
            Spacer()
            AutoSwitchToggle()
                .frame(width: 310)
        }
    }

    private var candidatesSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("绿色候选节点")
                    .font(.title3.weight(.semibold))
                Text("< 250 ms")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                if store.isRefreshingCandidates {
                    ProgressView().controlSize(.small)
                }
                Button("刷新") { Task { await store.refreshCandidates() } }
                    .disabled(store.isRefreshingCandidates)
            }
            if store.candidates.isEmpty {
                ContentUnavailableView("暂无候选数据", systemImage: "wifi.exclamationmark", description: Text("刷新后显示当前绿色节点排序。"))
                    .frame(height: 260)
            } else {
                Table(Array(store.candidates.prefix(20))) {
                    TableColumn("节点", value: \.name)
                    TableColumn("套餐") { candidate in
                        HStack(spacing: 4) {
                            Text(candidate.subscriptionName ?? "—")
                            if candidate.preferred == true {
                                Image(systemName: "star.fill").foregroundStyle(.yellow)
                            }
                        }
                    }
                    .width(105)
                    TableColumn("延迟") { candidate in Text("\(candidate.delayMS) ms") }
                        .width(70)
                    TableColumn("已验证") { candidate in
                        Image(systemName: candidate.knownSuccess ? "checkmark.seal.fill" : "minus.circle")
                            .foregroundStyle(candidate.knownSuccess ? .green : .secondary)
                    }
                    .width(60)
                }
                .frame(height: 280)
            }
            Text("最近测速：\(store.status.lastProbe?.nodeGuardDisplay ?? "—")")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
        .frame(maxWidth: .infinity)
    }

    private var activitySection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("后台状态")
                .font(.title3.weight(.semibold))
            LabeledContent("守护程序", value: store.status.daemonRunning ? "运行中" : "已停止")
            LabeledContent("Mihomo", value: store.status.mihomoVersion ?? "不可用")
            LabeledContent("恢复窗口切换", value: "\(store.status.switchesInWindow) / \(store.status.maxSwitches ?? 8)")
            LabeledContent("最近切换", value: store.status.lastSwitch?.nodeGuardDisplay ?? "—")
            LabeledContent("首选套餐", value: store.status.preferredSubscriptionName ?? "—")
            LabeledContent("实际套餐", value: store.status.currentSubscriptionName ?? "—")
            if let nextRecovery = store.status.nextRecovery, nextRecovery != .distantPast {
                LabeledContent("下次自动恢复", value: nextRecovery.nodeGuardDisplay)
            }
            LabeledContent("故障检测", value: "后台日志 · 250ms")
            if let signalAt = store.status.lastAgentSignalAt, signalAt != .distantPast {
                LabeledContent("最后 Agent 信号", value: signalAt.nodeGuardDisplay)
            }
            Divider()
            Text("冷却节点")
                .font(.headline)
            let cooldowns = (store.status.cooldowns ?? [:]).sorted { $0.value < $1.value }
            if cooldowns.isEmpty {
                Text("无")
                    .foregroundStyle(.secondary)
            } else {
                ForEach(cooldowns.prefix(5), id: \.key) { name, until in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(name).lineLimit(1)
                        Text("至 \(until.nodeGuardDisplay)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            if let error = store.status.lastError, !error.isEmpty {
                Divider()
                Text("最近状态")
                    .font(.headline)
                Text(error)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
            Spacer(minLength: 0)
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    private var controls: some View {
        HStack {
            Button("运行 Doctor") { Task { await store.runDoctor() } }
            Button("打开日志") { store.openLog() }
            Button("立即恢复并 Retry") { Task { await store.recoverNow() } }
                .buttonStyle(.borderedProminent)
                .disabled(store.isActionRunning || store.status.paused || !store.status.daemonRunning)
            if store.componentUpdateAvailable {
                Button("更新后台组件") { Task { await store.installService() } }
                    .disabled(store.isActionRunning)
            }
            Spacer()
            if !store.status.daemonRunning {
                Button("启动后台服务") { Task { await store.startService() } }
                    .buttonStyle(.borderedProminent)
                    .disabled(store.isActionRunning || !store.status.installed)
            }
            Button("停止后台服务", role: .destructive) { store.requestStopService() }
                .disabled(store.isActionRunning || !store.status.daemonRunning)
        }
    }

    private var errorBinding: Binding<Bool> {
        Binding(get: { store.errorMessage != nil }, set: { if !$0 { store.errorMessage = nil } })
    }
}
