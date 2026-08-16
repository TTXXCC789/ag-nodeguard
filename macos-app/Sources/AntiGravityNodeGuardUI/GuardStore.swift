@preconcurrency import AppKit
@preconcurrency import Foundation
import Observation

@MainActor
@Observable
final class GuardStore {
    private(set) var status = GuardStatus.unavailable
    private(set) var candidates: [Candidate] = []
    private(set) var subscriptions: [Subscription] = []
    private(set) var isActionRunning = false
    private(set) var isRefreshingCandidates = false
    private(set) var isRefreshingSubscriptions = false
    private(set) var isSelectingSubscription = false
    private(set) var doctorOutput = ""
    private(set) var componentUpdateAvailable = false
    private(set) var bundledComponentVersion: String?
    private(set) var installedComponentVersion: String?
    var errorMessage: String?
    var showingDoctor = false
    var confirmingStop = false

    let cli: GuardCLI
    private let installedCLI: GuardCLI
    private var streamProcess: Process?
    private var streamBuffer = Data()
    private var reconnectTask: Task<Void, Never>?
    private var candidateRefreshTask: Task<Void, Never>?
    private var subscriptionRefreshTask: Task<Void, Never>?
    private var reconnectDelay: UInt64 = 1
    private var started = false

    init(cliURL: URL) {
        self.cli = GuardCLI(executableURL: cliURL)
        let installedURL = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/AntiGravityNodeGuard/bin/ag-nodeguard")
        self.installedCLI = GuardCLI(executableURL: installedURL)
    }

    func start() {
        guard !started else { return }
        started = true
        launchStatusStream()
        candidateRefreshTask = Task { [weak self] in
            guard let self else { return }
            await checkComponentVersion()
            await refreshSubscriptions(force: false)
            await refreshCandidates()
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(300))
                guard !Task.isCancelled else { break }
                await refreshCandidates()
            }
        }
        subscriptionRefreshTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(30))
                guard !Task.isCancelled, let self else { break }
                await refreshSubscriptions(force: false)
            }
        }
    }

    func shutdownUI() {
        reconnectTask?.cancel()
        candidateRefreshTask?.cancel()
        subscriptionRefreshTask?.cancel()
        streamProcess?.terminationHandler = nil
        streamProcess?.terminate()
        streamProcess = nil
    }

    func setAutoSwitch(_ enabled: Bool) async {
        guard !isActionRunning else { return }
        isActionRunning = true
        defer { isActionRunning = false }
        do {
            if enabled {
                guard status.installed else {
                    throw GuardCLIError.commandFailed(code: 1, message: "请先安装后台服务。")
                }
                if !status.daemonRunning { _ = try await cli.run(.start) }
                _ = try await cli.run(.resume)
            } else {
                _ = try await cli.run(.pause)
            }
            try await refreshStatusOnce()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func installService() async {
        await performAction(.install)
        await checkComponentVersion()
        await restartStatusStream()
        await refreshSubscriptions(force: false)
        await refreshCandidates()
    }

    func checkComponentVersion() async {
        do {
            bundledComponentVersion = try await cli.run(.version).output
                .trimmingCharacters(in: .whitespacesAndNewlines)
        } catch {
            bundledComponentVersion = nil
        }
        do {
            installedComponentVersion = try await installedCLI.run(.version).output
                .trimmingCharacters(in: .whitespacesAndNewlines)
        } catch {
            installedComponentVersion = nil
        }
        updateComponentAvailability()
    }

    private func updateComponentAvailability() {
        componentUpdateAvailable = status.installed
            && bundledComponentVersion != nil
            && installedComponentVersion != bundledComponentVersion
    }

    func startService() async {
        await performAction(.start)
        await restartStatusStream()
    }

    func requestStopService() {
        confirmingStop = true
    }

    func stopService() async {
        confirmingStop = false
        await performAction(.stop)
        try? await Task.sleep(for: .milliseconds(250))
        try? await refreshStatusOnce()
    }

    func runDoctor() async {
        guard !isActionRunning else { return }
        isActionRunning = true
        defer { isActionRunning = false }
        do {
            doctorOutput = try await cli.run(.doctor).output
            showingDoctor = true
        } catch {
            doctorOutput = error.localizedDescription
            showingDoctor = true
        }
    }

    func recoverNow() async {
        await performAction(.recover)
        await refreshCandidates()
    }

    func refreshCandidates() async {
        guard !isRefreshingCandidates else { return }
        isRefreshingCandidates = true
        defer { isRefreshingCandidates = false }
        do {
            let result = try await cli.run(.candidates)
            candidates = try JSONDecoder.nodeGuard().decode([Candidate].self, from: Data(result.output.utf8))
        } catch {
            if status.installed { errorMessage = error.localizedDescription }
        }
    }

    func refreshSubscriptions(force: Bool) async {
        guard !isRefreshingSubscriptions else { return }
        isRefreshingSubscriptions = true
        defer { isRefreshingSubscriptions = false }
        do {
            let result = try await cli.run(.subscriptions(refresh: force))
            subscriptions = try JSONDecoder.nodeGuard().decode([Subscription].self, from: Data(result.output.utf8))
            if force {
                try await refreshStatusOnce()
                await refreshCandidates()
            }
        } catch {
            if status.installed { errorMessage = error.localizedDescription }
        }
    }

    func selectSubscription(_ uid: String) async {
        guard !isSelectingSubscription, !uid.isEmpty, uid != status.preferredSubscriptionUID else { return }
        isSelectingSubscription = true
        defer { isSelectingSubscription = false }
        do {
            _ = try await cli.run(.selectSubscription(uid))
            try await refreshStatusOnce()
            await refreshSubscriptions(force: false)
            await refreshCandidates()
        } catch {
            errorMessage = error.localizedDescription
            await refreshSubscriptions(force: false)
        }
    }

    func openLog() {
        let path = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Logs/AntiGravityNodeGuard/nodeguard.log")
        NSWorkspace.shared.open(path)
    }

    private func performAction(_ command: GuardCommand) async {
        guard !isActionRunning else { return }
        isActionRunning = true
        defer { isActionRunning = false }
        do {
            _ = try await cli.run(command)
            try await refreshStatusOnce()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func refreshStatusOnce() async throws {
        let result = try await cli.run(.status)
        status = try JSONDecoder.nodeGuard().decode(GuardStatus.self, from: Data(result.output.utf8))
        updateComponentAvailability()
    }

    private func restartStatusStream() async {
        streamProcess?.terminationHandler = nil
        streamProcess?.terminate()
        streamProcess = nil
        reconnectTask?.cancel()
        reconnectDelay = 1
        launchStatusStream()
    }

    private func launchStatusStream() {
        guard streamProcess == nil else { return }
        let process = Process()
        let output = Pipe()
        process.executableURL = cli.executableURL
        process.arguments = GuardCommand.statusStream.arguments
        process.standardOutput = output
        process.standardError = Pipe()
        output.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty else { return }
            Task { @MainActor [weak self] in self?.consumeStream(data) }
        }
        process.terminationHandler = { [weak self] _ in
            Task { @MainActor [weak self] in self?.statusStreamEnded() }
        }
        do {
            try process.run()
            streamProcess = process
        } catch {
            errorMessage = error.localizedDescription
            scheduleReconnect()
        }
    }

    private func consumeStream(_ data: Data) {
        streamBuffer.append(data)
        while let newline = streamBuffer.firstIndex(of: 0x0A) {
            let line = streamBuffer[..<newline]
            streamBuffer.removeSubrange(...newline)
            guard !line.isEmpty else { continue }
            do {
                status = try JSONDecoder.nodeGuard().decode(GuardStatus.self, from: Data(line))
                updateComponentAvailability()
                reconnectDelay = 1
            } catch {
                errorMessage = "状态数据解析失败：\(error.localizedDescription)"
            }
        }
    }

    private func statusStreamEnded() {
        streamProcess?.standardOutput = nil
        streamProcess = nil
        scheduleReconnect()
    }

    private func scheduleReconnect() {
        guard reconnectTask == nil else { return }
        let delay = reconnectDelay
        reconnectDelay = min(reconnectDelay * 2, 15)
        reconnectTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled, let self else { return }
            reconnectTask = nil
            launchStatusStream()
        }
    }
}
