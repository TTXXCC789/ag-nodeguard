import Foundation
import SwiftUI

@main
struct NodeGuardSelfTest {
    static func require(_ condition: @autoclosure () -> Bool, _ message: String) {
        guard condition() else {
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
            exit(1)
        }
    }

    static func main() throws {
        let statusJSON = #"{"observed_at":"2026-08-14T22:29:10.336507+08:00","installed":true,"paused":false,"daemon_running":true,"mihomo_version":"v1.19.29","current_node":"Example-04","current_delay_ms":94,"exit_region":"JP","antigravity_ok":true,"local_health_ok":true,"last_health_check":"2026-08-14T22:29:10+08:00","switches_in_window":1,"max_switches_per_window":8,"last_agent_signal":"backend_log_success","last_agent_signal_at":"2026-08-14T22:29:10+08:00","preferred_subscription_uid":"uid-a","preferred_subscription_name":"Primary Subscription","current_subscription_uid":"uid-b","current_subscription_name":"Backup Subscription","using_fallback":true,"subscription_scan_at":"2026-08-14T22:29:09+08:00","cooldowns":{}}"#
        let status = try JSONDecoder.nodeGuard().decode(GuardStatus.self, from: Data(statusJSON.utf8))
        require(status.currentNode == "Example-04", "status node decoding")
        require(status.currentDelayMS == 94, "status delay decoding")
        require(status.autoSwitchEnabled, "enabled state mapping")
        require(status.maxSwitches == 8 && status.lastAgentSignal == "backend_log_success", "backend signal decoding")
        require(status.preferredSubscriptionName == "Primary Subscription" && status.currentSubscriptionName == "Backup Subscription" && status.usingFallback == true, "subscription status decoding")
        require(status.visualState(now: status.observedAt.addingTimeInterval(2)) == .healthy, "healthy state mapping")

        var warning = GuardStatus.unavailable
        require(warning.visualState() == .offline, "unavailable state mapping")
        warning.installed = true
        warning.daemonRunning = true
        warning.paused = true
        warning.observedAt = Date()
        require(warning.visualState() == .warning, "paused state mapping")

        let candidateJSON = #"[{"name":"Example-01","type":"Hysteria2","delay_ms":95,"score":100,"cooldown_until":"0001-01-01T00:00:00Z","known_success":true,"subscription_uid":"uid-a","subscription_name":"Primary Subscription","preferred":true,"fallback":false},{"name":"Example-01","type":"VLESS","delay_ms":110,"score":200,"cooldown_until":"0001-01-01T00:00:00Z","known_success":false,"subscription_uid":"uid-b","subscription_name":"Backup Subscription","preferred":false,"fallback":true}]"#
        let candidates = try JSONDecoder.nodeGuard().decode([Candidate].self, from: Data(candidateJSON.utf8))
        require(candidates.count == 2 && candidates[0].delayMS == 95 && candidates[0].preferred == true, "candidate decoding")
        require(candidates[0].id != candidates[1].id, "duplicate node names use subscription-scoped IDs")
        require(candidates[0].cooldownUntil == .distantPast, "zero Go timestamp decoding")

        let subscriptionsJSON = #"[{"uid":"uid-a","name":"Primary Subscription","selected":true,"clash_active":true,"supported":true,"node_count":48,"eligible_node_count":48,"green_node_count":42},{"uid":"uid-provider","name":"Provider","selected":false,"clash_active":false,"supported":false,"unsupported_reason":"仅包含 proxy-provider，当前版本暂不支持","node_count":0,"eligible_node_count":0,"green_node_count":0}]"#
        let subscriptions = try JSONDecoder.nodeGuard().decode([Subscription].self, from: Data(subscriptionsJSON.utf8))
        require(subscriptions.count == 2 && subscriptions[0].selected && !subscriptions[1].supported, "subscription list decoding")

        require(GuardCommand.statusStream.arguments == ["status", "--watch", "--json", "--interval", "2s"], "status argument allowlist")
        require(GuardCommand.candidates.arguments == ["candidates", "--json"], "candidate argument allowlist")
        require(GuardCommand.subscriptions(refresh: false).arguments == ["subscriptions", "--json"], "subscription list argument allowlist")
        require(GuardCommand.subscriptions(refresh: true).arguments == ["subscriptions", "--refresh", "--json"], "subscription refresh argument allowlist")
        require(GuardCommand.selectSubscription("safe_uid").arguments == ["subscription", "select", "--uid", "safe_uid", "--json"], "subscription select argument allowlist")
        require(GuardCommand.recover.arguments == ["recover"], "recover argument allowlist")
        require(GuardCommand.pause.arguments == ["pause"] && GuardCommand.resume.arguments == ["resume"], "toggle argument allowlist")
        require(GuardCommand.status.timeout == 10 && GuardCommand.install.timeout == 45, "bounded command timeouts")

        print("Swift self-tests passed")
    }
}
