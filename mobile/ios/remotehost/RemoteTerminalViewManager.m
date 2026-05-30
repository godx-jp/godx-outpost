//
//  RemoteTerminalViewManager.m
//  Bridges the Swift RemoteTerminalViewManager + RemoteTerminalView to React
//  Native (old architecture): exports the view, its event props, and the
//  imperative feed/focus methods.
//
#import <React/RCTViewManager.h>
#import <React/RCTUIManager.h>

@interface RCT_EXTERN_MODULE(RemoteTerminalViewManager, RCTViewManager)

RCT_EXPORT_VIEW_PROPERTY(onData, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onSizeChange, RCTDirectEventBlock)
RCT_EXPORT_VIEW_PROPERTY(onReady, RCTDirectEventBlock)

RCT_EXTERN_METHOD(feed:(nonnull NSNumber *)reactTag base64:(NSString *)base64)
RCT_EXTERN_METHOD(focus:(nonnull NSNumber *)reactTag)

@end
