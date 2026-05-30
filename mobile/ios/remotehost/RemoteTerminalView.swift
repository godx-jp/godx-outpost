//
//  RemoteTerminalView.swift
//  Native terminal surface for the remote-host app, backed by SwiftTerm.
//
//  This is a pure display + input surface: bytes from our WebSocket are written
//  via `feedBase64`, and the bytes the user types (including IME-composed text
//  like Vietnamese Telex, handled natively by SwiftTerm's UITextInput) are
//  emitted through the `onData` event. SwiftTerm owns rendering, native scroll,
//  selection and the keyboard — fixing what xterm-in-WebView could not do.
//
import UIKit
import SwiftTerm

@objc(RemoteTerminalView)
class RemoteTerminalView: UIView, TerminalViewDelegate {
  private var terminalView: TerminalView!
  private var didReady = false

  // RN event blocks (wired by RemoteTerminalViewManager.m).
  @objc var onData: RCTDirectEventBlock?
  @objc var onSizeChange: RCTDirectEventBlock?
  @objc var onReady: RCTDirectEventBlock?

  override init(frame: CGRect) {
    super.init(frame: frame)
    setupTerminal()
  }

  required init?(coder: NSCoder) {
    super.init(coder: coder)
    setupTerminal()
  }

  private func setupTerminal() {
    let font = UIFont.monospacedSystemFont(ofSize: 13, weight: .regular)
    let tv = TerminalView(frame: bounds, font: font)
    tv.autoresizingMask = [.flexibleWidth, .flexibleHeight]
    tv.terminalDelegate = self
    // Dark theme matching the rest of the app (#0d0d0d / #e0e0e0).
    tv.nativeBackgroundColor = UIColor(red: 0x0d / 255.0, green: 0x0d / 255.0, blue: 0x0d / 255.0, alpha: 1)
    tv.nativeForegroundColor = UIColor(red: 0xe0 / 255.0, green: 0xe0 / 255.0, blue: 0xe0 / 255.0, alpha: 1)
    tv.backgroundColor = tv.nativeBackgroundColor
    addSubview(tv)
    terminalView = tv
  }

  override func layoutSubviews() {
    super.layoutSubviews()
    terminalView.frame = bounds
    // Signal readiness once we have a real size so JS can `attach` (which
    // replays scrollback). SwiftTerm reports the initial cols/rows via
    // sizeChanged after this.
    if !didReady, bounds.width > 0, bounds.height > 0 {
      didReady = true
      onReady?([:])
    }
  }

  // MARK: - Imperative API (called from the view manager)

  /// Write output bytes (base64-encoded) coming from the WebSocket into xterm.
  @objc func feedBase64(_ base64: String) {
    guard let data = Data(base64Encoded: base64) else { return }
    let bytes = [UInt8](data)
    terminalView.feed(byteArray: bytes[...])
  }

  /// Bring up / keep the on-screen keyboard (used after toolbar key presses).
  @objc func focusTerminal() {
    terminalView.becomeFirstResponder()
  }

  // MARK: - TerminalViewDelegate

  func send(source: TerminalView, data: ArraySlice<UInt8>) {
    onData?(["base64": Data(data).base64EncodedString()])
  }

  func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
    onSizeChange?(["cols": newCols, "rows": newRows])
  }

  func clipboardCopy(source: TerminalView, content: Data) {
    if let s = String(data: content, encoding: .utf8) {
      UIPasteboard.general.string = s
    }
  }

  // Unused delegate hooks (the host PTY drives everything else).
  func setTerminalTitle(source: TerminalView, title: String) {}
  func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
  func scrolled(source: TerminalView, position: Double) {}
  func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {}
  func bell(source: TerminalView) {}
  func iTermContent(source: TerminalView, content: ArraySlice<UInt8>) {}
  func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
}
