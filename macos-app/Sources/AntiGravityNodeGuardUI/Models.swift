import Foundation
import SwiftUI

struct GuardStatus: Codable, Sendable, Equatable {
    var observedAt: Date
    var installed: Bool
    var paused: Bool
    var daemonRunning: Bool
    var mihomoVersion: String?
    var currentNode: String?
    var currentDelayMS: Int?
    var exitIP: String?
    var exitRegion: String?
    var antigravityOK: Bool
    var localHealthOK: Bool?
    var lastHealthCheck: Date?
    var lastProbe: Date?
    var lastSwitch: Date?
    var nextRecovery: Date?
    var switchesInWindow: Int
    var lastFailureKind: String?
    var lastError: String?
    var lastAgentSignal: String?
    var lastAgentSignalAt: Date?
    var maxSwitches: Int?
    var preferredSubscriptionUID: String?
    var preferredSubscriptionName: String?
    var currentSubscriptionUID: String?
    var currentSubscriptionName: String?
    var usingFallback: Bool?
    var subscriptionScanAt: Date?
    var cooldowns: [String: Date]?

    enum CodingKeys: String, CodingKey {
        case observedAt = "observed_at"
        case installed, paused
        case daemonRunning = "daemon_running"
        case mihomoVersion = "mihomo_version"
        case currentNode = "current_node"
        case currentDelayMS = "current_delay_ms"
        case exitIP = "exit_ip"
        case exitRegion = "exit_region"
        case antigravityOK = "antigravity_ok"
        case localHealthOK = "local_health_ok"
        case lastHealthCheck = "last_health_check"
        case lastProbe = "last_probe"
        case lastSwitch = "last_switch"
        case nextRecovery = "next_recovery"
        case switchesInWindow = "switches_in_window"
        case lastFailureKind = "last_failure_kind"
        case lastError = "last_error"
        case lastAgentSignal = "last_agent_signal"
        case lastAgentSignalAt = "last_agent_signal_at"
        case maxSwitches = "max_switches_per_window"
        case preferredSubscriptionUID = "preferred_subscription_uid"
        case preferredSubscriptionName = "preferred_subscription_name"
        case currentSubscriptionUID = "current_subscription_uid"
        case currentSubscriptionName = "current_subscription_name"
        case usingFallback = "using_fallback"
        case subscriptionScanAt = "subscription_scan_at"
        case cooldowns
    }

    static let unavailable = GuardStatus(
        observedAt: .distantPast,
        installed: false,
        paused: true,
        daemonRunning: false,
        antigravityOK: false,
        localHealthOK: false,
        switchesInWindow: 0
    )

    var autoSwitchEnabled: Bool { installed && daemonRunning && !paused }

    func isStale(now: Date = Date()) -> Bool {
        now.timeIntervalSince(observedAt) > 6
    }

    func visualState(now: Date = Date()) -> GuardVisualState {
        if !installed || !daemonRunning || isStale(now: now) { return .offline }
        if paused || !antigravityOK { return .warning }
        return .healthy
    }
}

enum GuardVisualState: String, Sendable {
    case healthy
    case warning
    case offline

    var title: String {
        switch self {
        case .healthy: "运行正常"
        case .warning: "需要注意"
        case .offline: "后台离线"
        }
    }

    var color: Color {
        switch self {
        case .healthy: .green
        case .warning: .orange
        case .offline: .red
        }
    }

    var symbol: String {
        switch self {
        case .healthy: "antenna.radiowaves.left.and.right"
        case .warning: "pause.circle.fill"
        case .offline: "exclamationmark.triangle.fill"
        }
    }
}

struct Candidate: Codable, Sendable, Equatable, Identifiable {
    var name: String
    var type: String
    var delayMS: Int
    var score: Int64
    var cooldownUntil: Date?
    var knownSuccess: Bool
    var subscriptionUID: String?
    var subscriptionName: String?
    var preferred: Bool?
    var fallback: Bool?

    var id: String { "\(subscriptionUID ?? "legacy")\u{0}\(name)" }

    enum CodingKeys: String, CodingKey {
        case name, type, score
        case delayMS = "delay_ms"
        case cooldownUntil = "cooldown_until"
        case knownSuccess = "known_success"
        case subscriptionUID = "subscription_uid"
        case subscriptionName = "subscription_name"
        case preferred, fallback
    }
}

struct Subscription: Codable, Sendable, Equatable, Identifiable {
    var uid: String
    var name: String
    var selected: Bool
    var clashActive: Bool
    var supported: Bool
    var unsupportedReason: String?
    var nodeCount: Int
    var eligibleNodeCount: Int
    var greenNodeCount: Int
    var updatedAt: Date?

    var id: String { uid }

    enum CodingKeys: String, CodingKey {
        case uid, name, selected, supported
        case clashActive = "clash_active"
        case unsupportedReason = "unsupported_reason"
        case nodeCount = "node_count"
        case eligibleNodeCount = "eligible_node_count"
        case greenNodeCount = "green_node_count"
        case updatedAt = "updated_at"
    }
}

extension JSONDecoder {
    static func nodeGuard() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let raw = try container.decode(String.self)
            if raw.hasPrefix("0001-") { return .distantPast }
            let fractional = ISO8601DateFormatter()
            fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = fractional.date(from: raw) { return date }
            let regular = ISO8601DateFormatter()
            regular.formatOptions = [.withInternetDateTime]
            if let date = regular.date(from: raw) { return date }
            throw DecodingError.dataCorruptedError(in: container, debugDescription: "Invalid RFC3339 timestamp")
        }
        return decoder
    }
}

extension Date {
    var nodeGuardDisplay: String {
        if self == .distantPast { return "—" }
        return formatted(date: .abbreviated, time: .standard)
    }
}
