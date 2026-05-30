//
//  RemoteTerminalViewManager.swift
//  React Native (old architecture) view manager exposing RemoteTerminalView.
//
//  JS uses:
//    requireNativeComponent('RemoteTerminalView')           // the view
//    NativeModules.RemoteTerminalViewManager.feed(tag, b64)  // write output
//    NativeModules.RemoteTerminalViewManager.focus(tag)      // focus keyboard
//
import Foundation
import UIKit

@objc(RemoteTerminalViewManager)
class RemoteTerminalViewManager: RCTViewManager {
  override func view() -> UIView! {
    return RemoteTerminalView()
  }

  override static func requiresMainQueueSetup() -> Bool {
    return true
  }

  /// Write output bytes (base64) into the terminal identified by reactTag.
  @objc func feed(_ reactTag: NSNumber, base64: NSString) {
    bridge.uiManager.addUIBlock { _, viewRegistry in
      guard let view = viewRegistry?[reactTag] as? RemoteTerminalView else { return }
      view.feedBase64(base64 as String)
    }
  }

  /// Make the terminal first responder (keep the keyboard open after toolbar taps).
  @objc func focus(_ reactTag: NSNumber) {
    bridge.uiManager.addUIBlock { _, viewRegistry in
      guard let view = viewRegistry?[reactTag] as? RemoteTerminalView else { return }
      view.focusTerminal()
    }
  }
}
