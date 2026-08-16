@preconcurrency import Foundation
import Darwin

enum GuardCommand: Sendable, Equatable {
    case status
    case statusStream
    case candidates
    case subscriptions(refresh: Bool)
    case selectSubscription(String)
    case pause
    case resume
    case start
    case stop
    case doctor
    case recover
    case install
    case version

    var arguments: [String] {
        switch self {
        case .status: ["status", "--json"]
        case .statusStream: ["status", "--watch", "--json", "--interval", "2s"]
        case .candidates: ["candidates", "--json"]
        case .subscriptions(let refresh): refresh ? ["subscriptions", "--refresh", "--json"] : ["subscriptions", "--json"]
        case .selectSubscription(let uid): ["subscription", "select", "--uid", uid, "--json"]
        case .pause: ["pause"]
        case .resume: ["resume"]
        case .start: ["start"]
        case .stop: ["stop"]
        case .doctor: ["doctor"]
        case .recover: ["recover"]
        case .install: ["install"]
        case .version: ["version"]
        }
    }

    var timeout: TimeInterval {
        switch self {
        case .candidates, .doctor, .install, .recover, .subscriptions(refresh: true), .selectSubscription: 45
        default: 10
        }
    }
}

struct CommandResult: Sendable {
    let output: String
    let errorOutput: String
    let exitCode: Int32
}

enum GuardCLIError: LocalizedError, Equatable {
    case executableMissing
    case launchFailed(String)
    case timedOut
    case commandFailed(code: Int32, message: String)
    case invalidOutput

    var errorDescription: String? {
        switch self {
        case .executableMissing: "找不到内嵌的 ag-nodeguard 后台组件。"
        case .launchFailed(let message): "无法启动后台组件：\(message)"
        case .timedOut: "后台命令执行超时。"
        case .commandFailed(_, let message): message.isEmpty ? "后台命令执行失败。" : message
        case .invalidOutput: "后台组件返回了无法识别的数据。"
        }
    }
}

struct GuardCLI: Sendable {
    let executableURL: URL

    func run(_ command: GuardCommand) async throws -> CommandResult {
        let executableURL = executableURL
        return try await Task.detached(priority: .userInitiated) {
            guard FileManager.default.isExecutableFile(atPath: executableURL.path) else {
                throw GuardCLIError.executableMissing
            }
            let process = Process()
            let standardOutput = Pipe()
            let standardError = Pipe()
            process.executableURL = executableURL
            process.arguments = command.arguments
            process.standardOutput = standardOutput
            process.standardError = standardError
            do {
                try process.run()
            } catch {
                throw GuardCLIError.launchFailed(error.localizedDescription)
            }
            let deadline = Date().addingTimeInterval(command.timeout)
            while process.isRunning && Date() < deadline {
                usleep(30_000)
            }
            if process.isRunning {
                process.terminate()
                process.waitUntilExit()
                throw GuardCLIError.timedOut
            }
            let outputData = standardOutput.fileHandleForReading.readDataToEndOfFile()
            let errorData = standardError.fileHandleForReading.readDataToEndOfFile()
            let result = CommandResult(
                output: String(decoding: outputData, as: UTF8.self),
                errorOutput: String(decoding: errorData, as: UTF8.self),
                exitCode: process.terminationStatus
            )
            if result.exitCode != 0 {
                let message = result.errorOutput.trimmingCharacters(in: .whitespacesAndNewlines)
                throw GuardCLIError.commandFailed(code: result.exitCode, message: message)
            }
            return result
        }.value
    }
}
