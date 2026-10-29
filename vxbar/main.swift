import AppKit
import ServiceManagement

// vx — menu bar companion for the vx proxy daemon.
// Responsibilities:
//   * auto-start `vx serve` on launch and restart it if it dies
//   * status item in the macOS menu bar with Start/Stop + Start at Login
//   * LSUIElement app: clicking the .app starts everything in the background

let fm = FileManager.default
let home = NSHomeDirectory()

func candidateVXPaths() -> [String] {
    var list: [String] = []
    if let env = ProcessInfo.processInfo.environment["VX_BIN"] { list.append(env) }
    if let bundled = Bundle.main.path(forResource: "vx", ofType: nil) { list.append(bundled) }
    list += [
        home + "/.vx/bin/vx",
        "/opt/homebrew/bin/vx",
        "/usr/local/bin/vx",
    ]
    return list
}

func findVX() -> String? {
    return candidateVXPaths().first { fm.isExecutableFile(atPath: $0) }
}

func apiPort() -> Int {
    let path = home + "/.vx/config.json"
    if let data = fm.contents(atPath: path),
       let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
       let port = obj["api_port"] as? Int {
        return port
    }
    return 18082
}

func fetchStatus(completion: @escaping (Bool, [String: Any]?) -> Void) {
    guard let url = URL(string: "http://127.0.0.1:\(apiPort())/status") else {
        DispatchQueue.main.async { completion(false, nil) }
        return
    }
    var req = URLRequest(url: url)
    req.timeoutInterval = 0.8
    URLSession.shared.dataTask(with: req) { data, _, _ in
        var status: [String: Any]?
        if let d = data, let obj = try? JSONSerialization.jsonObject(with: d) as? [String: Any] {
            status = obj
        }
        let ok = (status?["ok"] as? Bool) ?? false
        DispatchQueue.main.async { completion(ok, status) }
    }.resume()
}

final class DaemonSupervisor {
    private(set) var process: Process?
    var userStopped = false
    private var restartWork: DispatchWorkItem?

    var isRunning: Bool { process?.isRunning ?? false }

    func start() {
        cancelRestart()
        guard let vx = findVX() else {
            NSLog("vxbar: vx binary not found")
            return
        }
        // Single-instance guard: if a daemon is already serving (e.g. started
        // from a terminal), adopt it instead of spawning a second one.
        fetchStatus { [weak self] ok, _ in
            guard let self else { return }
            if ok {
                NSLog("vxbar: daemon already running elsewhere — adopting it")
                self.userStopped = false
                return
            }
            self.spawnServe(vx: vx)
        }
    }

    private func spawnServe(vx: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: vx)
        p.arguments = ["serve"]

        let logPath = home + "/.vx/vxbar.log"
        if !fm.fileExists(atPath: logPath) {
            try? fm.createDirectory(atPath: home + "/.vx", withIntermediateDirectories: true)
            fm.createFile(atPath: logPath, contents: nil)
        }
        if let fh = FileHandle(forWritingAtPath: logPath) {
            fh.seekToEndOfFile()
            p.standardOutput = fh
            p.standardError = fh
        }
        p.terminationHandler = { [weak self] proc in
            let code = proc.terminationStatus
            DispatchQueue.main.async { self?.childExited(code) }
        }
        do {
            try p.run()
            process = p
        } catch {
            NSLog("vxbar: failed to start vx serve: \(error)")
        }
    }

    func stop() {
        userStopped = true
        cancelRestart()
        if let p = process, p.isRunning {
            p.terminate() // SIGTERM — vx serve shuts down xray + removes pidfile
        }
        process = nil
    }

    private func childExited(_ code: Int32) {
        process = nil
        guard !userStopped else { return }
        // If another instance (e.g. a terminal run) already owns the API port,
        // don't restart-loop against it.
        fetchStatus { ok, _ in
            if !ok { self.scheduleRestart() }
        }
    }

    private func scheduleRestart() {
        cancelRestart()
        let work = DispatchWorkItem { [weak self] in
            guard let self, !self.userStopped, !self.isRunning else { return }
            self.start()
        }
        restartWork = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 2, execute: work)
    }

    private func cancelRestart() {
        restartWork?.cancel()
        restartWork = nil
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    let daemon = DaemonSupervisor()
    var statusItem: NSStatusItem!
    var timer: Timer?
    var lastStatus: [String: Any]?

    var headerItem: NSMenuItem!
    var startStopItem: NSMenuItem!
    var loginItem: NSMenuItem!

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        setStatusIcon(up: false)

        let menu = NSMenu()
        menu.delegate = self
        menu.autoenablesItems = false

        headerItem = NSMenuItem(title: "vx proxy — starting…", action: nil, keyEquivalent: "")
        headerItem.isEnabled = false
        menu.addItem(headerItem)

        let details = NSMenuItem(title: "", action: nil, keyEquivalent: "")
        details.isEnabled = false
        details.tag = 100
        menu.addItem(details)

        menu.addItem(.separator())

        startStopItem = NSMenuItem(title: "Stop daemon", action: #selector(toggleDaemon), keyEquivalent: "s")
        startStopItem.target = self
        menu.addItem(startStopItem)

        loginItem = NSMenuItem(title: "Start at Login", action: #selector(toggleLogin), keyEquivalent: "")
        loginItem.target = self
        menu.addItem(loginItem)

        menu.addItem(.separator())

        let extItem = NSMenuItem(title: "Reinstall Chrome Extension", action: #selector(reinstallExtension), keyEquivalent: "")
        extItem.target = self
        menu.addItem(extItem)

        let openItem = NSMenuItem(title: "Open ~/.vx Folder", action: #selector(openConfigFolder), keyEquivalent: "")
        openItem.target = self
        menu.addItem(openItem)

        menu.addItem(.separator())

        let quitItem = NSMenuItem(title: "Quit vx", action: #selector(quitApp), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)

        statusItem.menu = menu

        autoStart()
        refreshStatus()
        timer = Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] _ in
            self?.refreshStatus()
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        daemon.stop()
    }

    // MARK: - Startup

    func autoStart() {
        fetchStatus { [weak self] ok, _ in
            guard let self else { return }
            if !ok {
                self.daemon.userStopped = false
                self.daemon.start()
            }
        }
    }

    // MARK: - Status

    func refreshStatus() {
        fetchStatus { [weak self] ok, status in
            guard let self else { return }
            if let s = status { self.lastStatus = s }
            if ok {
                self.daemon.userStopped = false
            } else if !self.daemon.userStopped && !self.daemon.isRunning {
                // Keep-alive: auto-recover whenever the daemon drops and user didn't explicitly stop it.
                self.daemon.start()
            }
            self.setStatusIcon(up: ok)
            self.updateHeader(ok: ok)
        }
    }

    func setStatusIcon(up: Bool) {
        let name = up ? "network" : "network.slash"
        let img = NSImage(systemSymbolName: name, accessibilityDescription: "vx proxy")
        img?.isTemplate = true
        statusItem.button?.image = img
        statusItem.button?.toolTip = up ? "vx proxy: running" : "vx proxy: stopped"
    }

    func updateHeader(ok: Bool) {
        if ok {
            headerItem.title = "vx proxy — running"
            startStopItem.title = daemon.isRunning ? "Stop daemon" : "Stop daemon (external)"
        } else {
            headerItem.title = "vx proxy — stopped"
            startStopItem.title = "Start daemon"
        }
        if let s = lastStatus {
            let mode = (s["mode"] as? String) ?? "?"
            let group = (s["group"] as? String) ?? "-"
            let xrayUp = (s["xray_running"] as? Bool) ?? false
            let nodes = (s["nodes"] as? [Any])?.count ?? 0
            let details = menuDetailsItem()
            details?.title = "\(group) · \(mode) · \(nodes) nodes · xray \(xrayUp ? "up" : "DOWN")"
        }
    }

    func menuDetailsItem() -> NSMenuItem? {
        return statusItem.menu?.items.first { $0.tag == 100 }
    }

    func menuWillOpen(_ menu: NSMenu) {
        refreshStatus()
        if #available(macOS 13.0, *) {
            loginItem.state = SMAppService.mainApp.status == .enabled ? .on : .off
        }
    }

    // MARK: - Actions

    @objc func toggleDaemon() {
        if daemon.isRunning {
            daemon.stop()
        } else if findVX() != nil {
            daemon.userStopped = false
            daemon.start()
        } else {
            showAlert("vx binary not found", "Build it with: cd vx && go build -o bin/vx ./cmd/vx")
            return
        }
        refreshStatus()
    }

    @objc func toggleLogin() {
        if #available(macOS 13.0, *) {
            do {
                if SMAppService.mainApp.status == .enabled {
                    try SMAppService.mainApp.unregister()
                } else {
                    try SMAppService.mainApp.register()
                }
            } catch {
                showAlert(
                    "Could not update login item",
                    "\(error.localizedDescription)\n\nAdd it manually: System Settings → General → Login Items → + → choose the vx app."
                )
            }
            loginItem.state = SMAppService.mainApp.status == .enabled ? .on : .off
        } else {
            showAlert(
                "Start at Login requires macOS 13+",
                "Add it manually: System Settings → General → Login Items → + → choose the vx app."
            )
        }
    }

    @objc func reinstallExtension() {
        guard let vx = findVX() else {
            showAlert("vx binary not found", "Build it first: cd vx && go build -o bin/vx ./cmd/vx")
            return
        }
        let dest = home + "/vx-extension"
        let p = Process()
        p.executableURL = URL(fileURLWithPath: vx)
        p.arguments = ["ext", "install", dest]
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = pipe
        p.terminationHandler = { proc in
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let out = String(data: data, encoding: .utf8) ?? ""
            DispatchQueue.main.async {
                if proc.terminationStatus == 0 {
                    self.showAlert("Extension installed", "Loaded into \(dest)\nOpen chrome://extensions → Reload vx.")
                } else {
                    self.showAlert("Extension install failed", out)
                }
            }
        }
        do { try p.run() } catch { showAlert("Failed to run vx", error.localizedDescription) }
    }

    @objc func openConfigFolder() {
        let dir = home + "/.vx"
        try? fm.createDirectory(atPath: dir, withIntermediateDirectories: true)
        NSWorkspace.shared.open(URL(fileURLWithPath: dir))
    }

    @objc func quitApp() {
        NSApp.terminate(nil)
    }

    func showAlert(_ title: String, _ text: String) {
        let a = NSAlert()
        a.messageText = title
        a.informativeText = text
        a.runModal()
    }
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AppDelegate()
app.delegate = delegate
app.run()
