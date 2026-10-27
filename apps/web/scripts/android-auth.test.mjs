import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";
import test from "node:test";
import { createRequire } from "node:module";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import ts from "typescript";

const requireWeb = createRequire(new URL("../package.json", import.meta.url));
const challengeOrigin = "https://heartbeat-checker.ru";

function jsonResponse(payload, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: () => "application/json" },
    json: async () => payload,
  };
}

function mobileMonitor(overrides = {}) {
  return {
    id: "monitor-1",
    checkType: "http",
    name: "Primary monitor",
    url: "https://service.example.test/health",
    host: "",
    port: 0,
    method: "GET",
    expectedStatus: 200,
    bodyContains: "",
    intervalMinutes: 5,
    failureThreshold: 2,
    timeZone: "UTC",
    enabled: true,
    version: 1,
    state: "UP",
    lastCheckedAt: "2026-10-21T12:00:00Z",
    lastHttpStatus: 200,
    lastLatencyMs: 25,
    nextCheckAt: "2026-10-21T12:05:00Z",
    notificationChannels: [],
    provider: { names: ["Test CDN"] },
    ...overrides,
  };
}

function mobileMonitorPage(monitors) {
  return {
    monitors,
    pagination: {
      page: 1,
      pageSize: 25,
      total: monitors.length,
      totalPages: 1,
    },
    summary: { total: monitors.length, up: monitors.length, down: 0 },
  };
}

function mobileEntitlement(overrides = {}) {
  return {
    quota: 10,
    enabledMonitorCount: 1,
    activeMonitorCount: 1,
    admin: null,
    packages: [],
    ...overrides,
  };
}

function mobileResult(
  id,
  errorCode = "request_failed",
  checkedAt = "2026-10-21T12:00:00Z",
) {
  return {
    id,
    monitorId: "monitor-1",
    outcome: "UP",
    checkedAt,
    httpStatus: 200,
    latencyMs: 25,
    errorCode,
  };
}

function flattenStyle(style) {
  if (!style) return {};
  if (Array.isArray(style))
    return Object.assign({}, ...style.map(flattenStyle));
  return typeof style === "object" ? style : {};
}

function createNativeMocks(reducedMotionInitially = false, motionRead = null) {
  const backHandlers = new Set();
  const appListeners = new Set();
  const motionListeners = new Set();
  let reducedMotion = reducedMotionInitially;
  let colorScheme = "light";
  const intervalCounts = { started: 0, cleared: 0 };
  const animatedValues = new Set();
  const animationLog = {
    started: 0,
    stopped: 0,
    pressIns: 0,
    pressOuts: 0,
    handles: [],
  };
  let manualAnimations = false;
  const Animated = {
    Value: class {
      constructor(value) {
        this.value = value;
        animatedValues.add(this);
      }
      setValue(value) {
        this.value = value;
      }
      stopAnimation() {
        for (const handle of animationLog.handles) {
          if (handle.value === this && handle.active) handle.stop();
        }
      }
      interpolate(config) {
        return config.outputRange[0];
      }
    },
    timing(value, config) {
      const handle = {
        value,
        config,
        active: false,
        finish() {
          if (!handle.active) return;
          handle.active = false;
          value.setValue(config.toValue);
        },
        stop() {
          if (!handle.active) return;
          handle.active = false;
          animationLog.stopped++;
        },
      };
      animationLog.handles.push(handle);
      return {
        start() {
          animationLog.started++;
          handle.active = true;
          if (!manualAnimations) handle.finish();
        },
        stop: handle.stop,
      };
    },
    sequence(steps) {
      return {
        start: () => steps.forEach((step) => step.start()),
      };
    },
    createAnimatedComponent: (component) => component,
    View: null,
    ScrollView: null,
    Text: ({
      children,
      accessibilityLabel,
      accessibilityLiveRegion,
      style,
      ...props
    }) =>
      React.createElement(
        "span",
        {
          ...props,
          ...(accessibilityLabel ? { "aria-label": accessibilityLabel } : {}),
          ...(accessibilityLiveRegion
            ? { "aria-live": accessibilityLiveRegion }
            : {}),
          style: flattenStyle(style),
        },
        children,
      ),
  };
  const AccessibilityInfo = {
    isReduceMotionEnabled: async () => motionRead ?? reducedMotion,
    addEventListener(_event, listener) {
      motionListeners.add(listener);
      return { remove: () => motionListeners.delete(listener) };
    },
    setReducedMotion(value) {
      reducedMotion = value;
      for (const listener of [...motionListeners]) listener(value);
    },
  };
  const AppState = {
    currentState: "active",
    addEventListener(_event, listener) {
      appListeners.add(listener);
      return { remove: () => appListeners.delete(listener) };
    },
    setCurrentState(value) {
      this.currentState = value;
      for (const listener of [...appListeners]) listener(value);
    },
  };
  const BackHandler = {
    addEventListener(_event, listener) {
      backHandlers.add(listener);
      return { remove: () => backHandlers.delete(listener) };
    },
  };
  const component = (tag, testId) => {
    function MockNativeComponent(props) {
      const {
        children,
        style,
        contentContainerStyle,
        refreshControl,
        accessibilityLabel,
        accessibilityRole,
        accessibilityState,
        accessibilityLiveRegion,
        ...nativeProps
      } = props;
      delete nativeProps.keyboardShouldPersistTaps;
      delete nativeProps.onLayout;
      delete nativeProps.accessible;
      delete nativeProps.selectable;
      const css = flattenStyle(style ?? contentContainerStyle);
      if (Array.isArray(css.transform)) css.transform = "none";
      const attrs = {
        ...(testId ? { "data-native-component": testId } : {}),
        ...(accessibilityLabel ? { "aria-label": accessibilityLabel } : {}),
        ...(accessibilityRole ? { role: accessibilityRole } : {}),
        ...(accessibilityLiveRegion
          ? { "aria-live": accessibilityLiveRegion }
          : {}),
        ...(accessibilityState?.disabled ? { "aria-disabled": "true" } : {}),
        ...(css.minHeight !== undefined
          ? { "data-min-height": String(css.minHeight) }
          : {}),
        style: css,
        ...nativeProps,
      };
      return React.createElement(tag, attrs, children, refreshControl ?? null);
    }
    MockNativeComponent.displayName = `Mock${tag}`;
    return MockNativeComponent;
  };
  const TextInput = (props) => {
    const {
      value,
      onChangeText,
      secureTextEntry,
      accessibilityLabel,
      keyboardType,
      style,
      ...inputProps
    } = props;
    delete inputProps.accessibilityHint;
    delete inputProps.autoCapitalize;
    delete inputProps.autoCorrect;
    delete inputProps.placeholderTextColor;
    delete inputProps.returnKeyType;
    delete inputProps.textContentType;
    delete inputProps.onSubmitEditing;
    const editable = inputProps.editable;
    delete inputProps.editable;
    return React.createElement("input", {
      ...inputProps,
      "aria-label": accessibilityLabel,
      type: secureTextEntry ? "password" : "text",
      inputMode: keyboardType === "email-address" ? "email" : undefined,
      disabled: editable === false,
      style: flattenStyle(style),
      value,
      onInput: (event) => onChangeText?.(event.target.value),
    });
  };
  const Pressable = ({
    children,
    onPress,
    disabled,
    accessibilityLabel,
    accessibilityRole,
    accessibilityState,
    style,
    onPressIn,
    onPressOut,
    ...props
  }) => {
    const resolvedStyle =
      typeof style === "function"
        ? style({ pressed: false, hovered: false, focused: false })
        : style;
    return React.createElement(
      "button",
      {
        ...props,
        type: "button",
        onClick: onPress,
        onMouseDown: (event) => {
          animationLog.pressIns++;
          onPressIn?.(event);
        },
        onMouseUp: (event) => {
          animationLog.pressOuts++;
          onPressOut?.(event);
        },
        onMouseLeave: (event) => onPressOut?.(event),
        disabled,
        "aria-label": accessibilityLabel,
        "aria-pressed": accessibilityState?.selected,
        "aria-expanded": accessibilityState?.expanded,
        "aria-disabled": accessibilityState?.disabled ? "true" : undefined,
        role: accessibilityRole,
        style: flattenStyle(resolvedStyle),
      },
      children,
    );
  };
  const Switch = ({ value, onValueChange, disabled }) =>
    React.createElement("input", {
      type: "checkbox",
      checked: value,
      disabled,
      onChange: (event) => onValueChange?.(event.target.checked),
    });
  const Image = ({ accessibilityLabel, accessible, source, style, ...props }) =>
    React.createElement("img", {
      ...props,
      "data-source": source?.uri,
      ...(accessible === false ? { "aria-hidden": "true" } : {}),
      alt: accessibilityLabel ?? "",
      style: flattenStyle(style),
    });
  const RefreshControl = ({ refreshing, onRefresh }) =>
    React.createElement("button", {
      type: "button",
      "aria-label": "Обновить страницу",
      "data-refreshing": refreshing ? "true" : "false",
      onClick: onRefresh,
    });
  Animated.View = component("div");
  Animated.ScrollView = component("div", "ScrollView");
  const WebView = React.forwardRef(function MockWebView(props, ref) {
    React.useImperativeHandle(
      ref,
      () => ({
        stopLoading() {
          props.onStopLoading?.();
        },
      }),
      [props],
    );
    globalThis.__authWebViewProps = props;
    return React.createElement("div", {
      "data-testid": "auth-webview",
      "data-uri": props.source?.uri,
    });
  });
  return {
    reactNative: {
      AccessibilityInfo,
      Animated,
      ActivityIndicator: component("span"),
      Alert: { alert() {} },
      AppState,
      BackHandler,
      Image,
      KeyboardAvoidingView: component("section"),
      Pressable,
      RefreshControl,
      PermissionsAndroid: {
        PERMISSIONS: { POST_NOTIFICATIONS: "POST_NOTIFICATIONS" },
        RESULTS: { GRANTED: "granted" },
        request: async () => "granted",
      },
      Platform: { Version: 35, OS: "android" },
      ScrollView: component("div", "ScrollView"),
      StyleSheet: { create: (styles) => styles },
      Switch,
      Text: component("span"),
      TextInput,
      View: component("div"),
      useColorScheme: () => colorScheme,
    },
    webView: { WebView },
    backHandler: BackHandler,
    pressBack() {
      for (const listener of [...backHandlers]) listener();
    },
    AccessibilityInfo,
    AppState,
    animatedValues,
    animationLog,
    setManualAnimations(value) {
      manualAnimations = value;
    },
    intervalCounts,
    setColorScheme(value) {
      colorScheme = value;
    },
  };
}

function compile(relativePath, aliases, runtime = {}) {
  const source = fs.readFileSync(
    new URL(relativePath, import.meta.url),
    "utf8",
  );
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;
  const fixtureModule = { exports: {} };
  const exported = fixtureModule.exports;
  const runtimeRequire = runtime.require;
  const runtimeValues = { ...runtime };
  delete runtimeValues.require;
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports: exported,
    require(id) {
      if (Object.hasOwn(aliases, id)) return aliases[id];
      if (runtimeRequire) return runtimeRequire(id);
      return requireWeb(id);
    },
    console,
    URL,
    URLSearchParams,
    Date,
    Promise,
    AbortController,
    DOMException,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    ...runtimeValues,
  });
  return fixtureModule.exports;
}

async function createHarness({
  resetToken,
  tokens = ["nonce_1", "nonce_2"],
  onAuthed = async () => {},
  reducedMotion = false,
  motionRead = null,
} = {}) {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", {
    url: "https://heartbeat-checker.ru/login",
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.__authWebViewProps = null;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const mocks = createNativeMocks(reducedMotion, motionRead);
  const requests = [];
  let monitorContractActive = false;
  let monitorEntitlementOverride = null;
  const native = {
    API_ORIGIN: challengeOrigin,
    CHALLENGE_ORIGIN: challengeOrigin,
    authEpoch: async () => 0,
    restore: async () => null,
    DEV_CLIENT: false,
    pushStatus: async () => ({
      enabled: false,
      optedOut: false,
      provider: "",
      status: "",
    }),
    logoutCalls: 0,
    logout: async () => {
      native.logoutCalls++;
    },
    randomToken: async () => tokens.shift() ?? "nonce_extra",
    restoreRegistration: async () => null,
    saveRegistration: async () => {},
    clearRegistration: async () => {},
    restoreMonitorDraft: async () => null,
    saveMonitorDraft: async () => {},
    clearMonitorDraft: async () => {},
    ...mocks.native,
  };
  let fetchImpl = async (url, options) => {
    requests.push({ url, options });
    return {
      ok: true,
      status: 200,
      headers: { get: () => "application/json" },
      json: async () => ({ account: {}, session: {} }),
    };
  };
  const apiModule = compile(
    "../../mobile/src/api.ts",
    { "./native": { native }, "./types": {} },
    {
      fetch: (...args) => fetchImpl(...args),
    },
  );
  const monitorDraftModule = compile("../../mobile/src/MonitorDraft.tsx", {
    "react-native": mocks.reactNative,
    "./api": apiModule,
    "./native": { native },
    "./types": {},
  });
  const uiModule = compile(
    "../../mobile/src/ui.tsx",
    {
      "react-native": mocks.reactNative,
    },
    {
      setInterval(callback, delay) {
        mocks.intervalCounts.started++;
        return globalThis.setInterval(callback, delay);
      },
      clearInterval(id) {
        mocks.intervalCounts.cleared++;
        globalThis.clearInterval(id);
      },
      require(id) {
        if (id.endsWith(".png")) return { uri: id };
        return requireWeb(id);
      },
    },
  );
  const renderWithMotionPreferences = (element) =>
    root.render(React.createElement(uiModule.MotionPreferences, null, element));
  const authModule = compile(
    "../../mobile/src/AuthScreen.tsx",
    {
      "react-native": mocks.reactNative,
      "react-native-webview": mocks.webView,
      "./api": apiModule,
      "./native": { native },
      "./types": {},
      "./ui": uiModule,
    },
    {
      require(id) {
        if (id.endsWith(".png")) return { uri: id };
        return requireWeb(id);
      },
    },
  );
  await act(async () =>
    renderWithMotionPreferences(
      React.createElement(authModule.AuthScreen, { onAuthed, resetToken }),
    ),
  );
  await act(async () => Promise.resolve());
  return {
    dom,
    host,
    requests,
    native,
    uiModule,
    nativeMocks: mocks,
    async renderPage(refresh, motionKey) {
      await act(async () =>
        renderWithMotionPreferences(
          React.createElement(
            uiModule.Page,
            { title: "Обновление", refresh, motionKey },
            React.createElement("span", null, "Содержимое"),
          ),
        ),
      );
      await act(async () => Promise.resolve());
    },
    async renderPublicScreen({
      onLogin = async () => {},
      onRegister = async () => {},
      onHelp = async () => {},
      timerRuntime = {},
    } = {}) {
      const helpModule = compile(
        "../../mobile/src/HelpScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./ui": uiModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      const publicModule = compile(
        "../../mobile/src/PublicScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./HelpScreen": helpModule,
          "./ui": uiModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
          ...timerRuntime,
        },
      );
      await act(async () =>
        renderWithMotionPreferences(
          React.createElement(publicModule.PublicScreen, {
            onLogin,
            onRegister,
            onHelp,
          }),
        ),
      );
      await act(async () => Promise.resolve());
      return publicModule.PublicScreen;
    },
    async renderAccountScreen(onLogout = async () => {}) {
      const helpModule = compile(
        "../../mobile/src/HelpScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./ui": uiModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      const notificationsModule = compile(
        "../../mobile/src/NotificationsScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./types": {},
          "./ui": uiModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      const pushTestModule = compile("../../mobile/src/PushTestPanel.tsx", {
        "react-native": mocks.reactNative,
        "./api": apiModule,
        "./native": { native },
        "./ui": uiModule,
      });
      const accountModule = compile(
        "../../mobile/src/AccountScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./types": {},
          "./ui": uiModule,
          "./HelpScreen": helpModule,
          "./AuthScreen": authModule,
          "./NotificationsScreen": notificationsModule,
          "./PushTestPanel": pushTestModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      await act(async () =>
        renderWithMotionPreferences(
          React.createElement(accountModule.AccountScreen, { onLogout }),
        ),
      );
      await act(async () => Promise.resolve());
      await act(async () => Promise.resolve());
      return accountModule.AccountScreen;
    },
    async renderMonitorsScreen(
      deepLink = null,
      { onBuyQuota = async () => {}, onDeepLinkConsumed = () => {} } = {},
    ) {
      monitorContractActive = true;
      const notificationsModule = compile(
        "../../mobile/src/NotificationsScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./types": {},
          "./ui": uiModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      const monitorInsightsModule = compile(
        "../../mobile/src/MonitorInsights.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./types": {},
          "./ui": uiModule,
        },
      );
      const monitorsModule = compile(
        "../../mobile/src/MonitorsScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./types": {},
          "./ui": uiModule,
          "./NotificationsScreen": notificationsModule,
          "./MonitorDraft": monitorDraftModule,
          "./MonitorInsights": monitorInsightsModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      await act(async () =>
        renderWithMotionPreferences(
          React.createElement(
            monitorDraftModule.MonitorDraftProvider,
            { auth: apiModule.session() },
            React.createElement(monitorsModule.MonitorsScreen, {
              deepLink,
              onBuyQuota,
              onDeepLinkConsumed,
            }),
          ),
        ),
      );
      await act(async () => Promise.resolve());
      await act(async () => Promise.resolve());
      return monitorsModule.MonitorsScreen;
    },
    async renderBillingScreen(returnOrder = null, timerRuntime = {}) {
      monitorContractActive = false;
      const billingModule = compile(
        "../../mobile/src/BillingScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./types": {},
          "./ui": uiModule,
          "./MonitorDraft": monitorDraftModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
          ...timerRuntime,
        },
      );
      await apiModule.restoreSession();
      await act(async () =>
        renderWithMotionPreferences(
          React.createElement(billingModule.BillingScreen, { returnOrder }),
        ),
      );
      await act(async () => Promise.resolve());
      await act(async () => Promise.resolve());
      return billingModule.BillingScreen;
    },
    async renderDraftCheckoutFlow(onNavigateBilling = () => {}) {
      monitorContractActive = true;
      const notificationsModule = compile(
        "../../mobile/src/NotificationsScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./types": {},
          "./ui": uiModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      const monitorInsightsModule = compile(
        "../../mobile/src/MonitorInsights.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./types": {},
          "./ui": uiModule,
        },
      );
      const monitorsModule = compile(
        "../../mobile/src/MonitorsScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./types": {},
          "./ui": uiModule,
          "./NotificationsScreen": notificationsModule,
          "./MonitorDraft": monitorDraftModule,
          "./MonitorInsights": monitorInsightsModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      const billingModule = compile(
        "../../mobile/src/BillingScreen.tsx",
        {
          "react-native": mocks.reactNative,
          "./api": apiModule,
          "./native": { native },
          "./types": {},
          "./ui": uiModule,
          "./MonitorDraft": monitorDraftModule,
        },
        {
          require(id) {
            if (id.endsWith(".png")) return { uri: id };
            return requireWeb(id);
          },
        },
      );
      function DraftCheckoutNavigation() {
        const [tab, setTab] = React.useState("monitors");
        const [resumeDraft, setResumeDraft] = React.useState(0);
        return tab === "monitors"
          ? React.createElement(monitorsModule.MonitorsScreen, {
              resumeDraft,
              onBuyQuota: () => {
                onNavigateBilling();
                setTab("billing");
              },
            })
          : React.createElement(billingModule.BillingScreen, {
              onContinueMonitor: () => {
                setResumeDraft((value) => value + 1);
                setTab("monitors");
              },
            });
      }
      await act(async () =>
        renderWithMotionPreferences(
          React.createElement(
            monitorDraftModule.MonitorDraftProvider,
            { auth: apiModule.session() },
            React.createElement(DraftCheckoutNavigation),
          ),
        ),
      );
      await act(async () => Promise.resolve());
      await act(async () => Promise.resolve());
      return { monitorsModule, billingModule };
    },
    async renderDraftProvider(children, auth = apiModule.session(), key) {
      await act(async () =>
        renderWithMotionPreferences(
          React.createElement(
            monitorDraftModule.MonitorDraftProvider,
            { auth, key },
            children,
          ),
        ),
      );
      await act(async () => Promise.resolve());
    },
    apiModule,
    monitorDraftModule,
    pressBack: mocks.pressBack,
    setFetch(fn) {
      fetchImpl = async (url, options) => {
        const path = new URL(url).pathname.replace("/api/v1", "");
        if (monitorContractActive && path === "/entitlement")
          return jsonResponse({
            entitlement: monitorEntitlementOverride ?? mobileEntitlement(),
          });
        if (
          monitorContractActive &&
          /^\/monitors\/[^/]+\/statistics$/.test(path)
        )
          return jsonResponse({
            statistics: {
              monitorId: "monitor-1",
              periodDays: 7,
              from: "2026-10-15",
              to: "2026-10-21",
              generatedAt: "2026-10-21T12:00:00Z",
              timeZone: "UTC",
              coverage: {
                firstRecordedDay: "2026-10-21",
                daysWithData: 1,
                scheduleCompleteness: "unknown",
              },
              totals: {
                up: 1,
                down: 0,
                unknown: 0,
                total: 1,
                successfulCheckPercent: 100,
                averageLatencyMs: 25,
                latencySampleCount: 1,
              },
              days: [],
            },
          });
        return fn(url, options);
      };
    },
    setMonitorEntitlement(entitlement) {
      monitorEntitlementOverride = entitlement;
    },
    async click(label) {
      const matches = [
        ...host.querySelectorAll('button, [role="button"]'),
      ].filter(
        (item) =>
          item.getAttribute("aria-label") === label ||
          item.textContent?.trim() === label,
      );
      assert.equal(matches.length, 1, `expected one button named: ${label}`);
      const [button] = matches;
      await act(async () => button.click());
    },
    async mouse(label, type, index = 0) {
      const matches = [
        ...host.querySelectorAll('button, [role="button"]'),
      ].filter(
        (item) =>
          item.getAttribute("aria-label") === label ||
          item.textContent?.trim() === label,
      );
      assert.ok(matches.length > index, `button not found: ${label}`);
      await act(async () =>
        matches[index].dispatchEvent(
          new dom.window.MouseEvent(type, { bubbles: true }),
        ),
      );
    },
    async clickAll(label) {
      const matches = [
        ...host.querySelectorAll('button, [role="button"]'),
      ].filter(
        (item) =>
          item.getAttribute("aria-label") === label ||
          item.textContent?.trim() === label,
      );
      assert.ok(matches.length > 0, `button not found: ${label}`);
      for (const button of matches) await act(async () => button.click());
      return matches.length;
    },
    byRole(role, name, { exactlyOne = true } = {}) {
      const matches = [...host.querySelectorAll(`[role="${role}"]`)].filter(
        (item) =>
          item.getAttribute("aria-label") === name ||
          item.textContent?.trim() === name,
      );
      assert.ok(matches.length > 0, `${role} not found: ${name}`);
      if (exactlyOne)
        assert.equal(matches.length, 1, `expected one ${role} named: ${name}`);
      return matches[0];
    },
    async fill(label, value) {
      const input = host.querySelector(`input[aria-label="${label}"]`);
      assert.ok(input, `input not found: ${label}`);
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(
          dom.window.HTMLInputElement.prototype,
          "value",
        ).set;
        setter.call(input, value);
        input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
        input.dispatchEvent(new dom.window.Event("change", { bubbles: true }));
      });
    },
    async flush() {
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
    },
    async unmount() {
      await act(async () => root.unmount());
      dom.window.close();
      delete globalThis.window;
      delete globalThis.document;
      delete globalThis.IS_REACT_ACT_ENVIRONMENT;
      delete globalThis.__authWebViewProps;
    },
  };
}

function loadAndroidBridge() {
  const source = fs.readFileSync(
    new URL("../src/shared/android-bridge/index.ts", import.meta.url),
    "utf8",
  );
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  const fixtureModule = { exports: {} };
  vm.runInNewContext(compiled, {
    module: fixtureModule,
    exports: fixtureModule.exports,
    Response,
    process: { env: { NEXT_PUBLIC_TURNSTILE_SITE_KEY: "test-site-key" } },
  });
  return fixtureModule.exports.androidBridge;
}

function fakeClock(window) {
  let now = 0;
  let nextId = 1;
  const timers = new Map();
  window.setTimeout = (callback, delay = 0) => {
    const id = nextId++;
    timers.set(id, { callback, due: now + delay, interval: 0 });
    return id;
  };
  window.setInterval = (callback, delay = 0) => {
    const id = nextId++;
    timers.set(id, { callback, due: now + delay, interval: delay || 1 });
    return id;
  };
  window.clearTimeout = (id) => timers.delete(id);
  window.clearInterval = (id) => timers.delete(id);
  return {
    advance(milliseconds) {
      const end = now + milliseconds;
      while (true) {
        const due = [...timers.entries()]
          .filter(([, timer]) => timer.due <= end)
          .sort((left, right) => left[1].due - right[1].due)[0];
        if (!due) break;
        const [id, timer] = due;
        now = timer.due;
        if (timer.interval) timer.due += timer.interval;
        else timers.delete(id);
        timer.callback();
      }
      now = end;
    },
  };
}

function fakeTimerRuntime() {
  let now = Date.now();
  let nextId = 1;
  const timers = new Map();
  class FakeDate extends Date {
    static now() {
      return now;
    }
  }
  return {
    Date: FakeDate,
    setTimeout(callback, delay = 0) {
      const id = nextId++;
      timers.set(id, { callback, due: now + delay });
      return id;
    },
    clearTimeout(id) {
      timers.delete(id);
    },
    advance(milliseconds) {
      const end = now + milliseconds;
      while (true) {
        const due = [...timers.entries()]
          .filter(([, timer]) => timer.due <= end)
          .sort((left, right) => left[1].due - right[1].due)[0];
        if (!due) break;
        const [id, timer] = due;
        timers.delete(id);
        now = timer.due;
        timer.callback();
      }
      now = end;
    },
  };
}

function fakePublicTimerRuntime() {
  let now = Date.now();
  let nextId = 1;
  const timers = new Map();
  class FakeDate extends Date {
    static now() {
      return now;
    }
  }
  const runtime = {
    Date: FakeDate,
    setTimeout(callback, delay = 0) {
      const id = nextId++;
      timers.set(id, { callback, due: now + delay, interval: 0 });
      return id;
    },
    clearTimeout(id) {
      timers.delete(id);
    },
    setInterval(callback, delay = 0) {
      const id = nextId++;
      timers.set(id, {
        callback,
        due: now + Math.max(delay, 1),
        interval: Math.max(delay, 1),
      });
      return id;
    },
    clearInterval(id) {
      timers.delete(id);
    },
  };
  return {
    runtime,
    advance(milliseconds) {
      const end = now + milliseconds;
      while (true) {
        const due = [...timers.entries()]
          .filter(([, timer]) => timer.due <= end)
          .sort((left, right) => left[1].due - right[1].due)[0];
        if (!due) break;
        const [id, timer] = due;
        now = timer.due;
        if (timer.interval) timer.due += timer.interval;
        else timers.delete(id);
        timer.callback();
      }
      now = end;
    },
    pendingCount() {
      return timers.size;
    },
  };
}

async function createWebChallenge({
  url,
  state = null,
  turnstile = null,
} = {}) {
  const response = loadAndroidBridge()("/mobile/challenge");
  const html = await response.text();
  const dom = new JSDOM(html, {
    url:
      url ??
      `https://heartbeat-checker.ru/mobile/challenge?action=login&requestId=${"A".repeat(43)}`,
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  if (state)
    dom.window.history.replaceState(state, "", dom.window.location.href);
  const messages = [];
  const clock = fakeClock(dom.window);
  dom.window.ReactNativeWebView = {
    postMessage: (message) => messages.push(JSON.parse(message)),
  };
  if (turnstile) dom.window.turnstile = turnstile;
  dom.window.eval(
    fs.readFileSync(
      new URL("../public/android-bridge.js", import.meta.url),
      "utf8",
    ),
  );
  return { dom, response, messages, clock };
}

test("auth screens expose password visibility and preserve the shared card height", async () => {
  const login = await createHarness();
  try {
    const assertSharedBrand = () =>
      assert.ok(
        login.host.querySelector('img[data-source="../assets/logo.png"]'),
        "every auth state keeps the shared Heartbeat brand mark",
      );
    assertSharedBrand();
    assert.equal(
      login.host.querySelector('input[aria-label="Пароль"]').type,
      "password",
    );
    assert.equal(
      login.host.querySelector('[aria-label="Показать пароль"]') !== null,
      true,
    );
    assert.equal(
      login.host.querySelector('[data-min-height="700"]') !== null,
      true,
    );
    await login.click("Показать пароль");
    assert.equal(
      login.host.querySelector('input[aria-label="Пароль"]').type,
      "text",
    );
    assert.equal(
      login.host.querySelector('[aria-label="Скрыть пароль"]') !== null,
      true,
    );
    await login.click("Регистрация");
    assertSharedBrand();
    assert.ok(login.host.textContent.includes("Регистрация"));
    assert.ok(login.host.querySelector('input[aria-label="Имя"]'));
    assert.ok(login.host.querySelector('[data-min-height="700"]'));
    await login.click("Ко входу");
    await login.click("Забыли пароль?");
    assertSharedBrand();
    assert.ok(login.host.textContent.includes("Восстановление доступа"));
    assert.ok(login.host.querySelector('[data-min-height="700"]'));
  } finally {
    await login.unmount();
  }

  const reset = await createHarness({ resetToken: "reset-token" });
  try {
    assert.ok(
      reset.host.querySelector('img[data-source="../assets/logo.png"]'),
    );
    assert.ok(reset.host.textContent.includes("Новый пароль"));
    assert.equal(
      reset.host.querySelector('input[aria-label="Новый пароль"]').type,
      "password",
    );
    assert.ok(reset.host.querySelector('[data-min-height="700"]'));
  } finally {
    await reset.unmount();
  }
});

test("Yandex and VK sign-in marks share a 24px slot with normalized image sizes", async () => {
  const harness = await createHarness();
  try {
    const providerButton = (name) =>
      [...harness.host.querySelectorAll("button")].find((button) =>
        button.textContent.includes(name),
      );
    const yandex = providerButton("Войти через Яндекс")?.querySelector(
      'img[data-source="../assets/yandex.png"]',
    );
    const vk = providerButton("Войти через VK")?.querySelector(
      'img[data-source="../assets/vk.png"]',
    );
    assert.ok(yandex, "Yandex sign-in has its brand mark");
    assert.ok(vk, "VK sign-in has its brand mark");

    const yandexSlot = yandex.parentElement;
    const vkSlot = vk.parentElement;
    assert.equal(yandexSlot.style.width, "24px");
    assert.equal(yandexSlot.style.height, "24px");
    assert.equal(vkSlot.style.width, "24px");
    assert.equal(vkSlot.style.height, "24px");
    assert.equal(yandex.style.width, "24px");
    assert.equal(yandex.style.height, "24px");
    assert.equal(vk.style.width, "32px");
    assert.equal(vk.style.height, "32px");
    assert.equal(vkSlot.style.overflow, "hidden");
  } finally {
    await harness.unmount();
  }
});

test("public landing routes its accessible login, registration, and help actions", async () => {
  const harness = await createHarness();
  const actions = { login: 0, register: 0, help: 0 };
  try {
    await harness.renderPublicScreen({
      onLogin: () => actions.login++,
      onRegister: () => actions.register++,
      onHelp: () => actions.help++,
    });
    harness.byRole(
      "header",
      "Круглосуточный мониторинг ваших сайтов и сервисов.",
    );
    assert.doesNotMatch(
      harness.host.textContent,
      /Статичная демонстрация с вымышленными данными/,
    );
    const demoRows = [
      ...harness.host.querySelectorAll("button[aria-label]"),
    ].filter((button) =>
      button.getAttribute("aria-label").includes("Успешные проверки за 7 дней"),
    );
    assert.equal(
      demoRows.length,
      3,
      "the public demo shows all three monitors",
    );
    const apiRow = demoRows.find((button) =>
      button.getAttribute("aria-label").startsWith("API магазина."),
    );
    assert.ok(apiRow);
    assert.equal(apiRow.getAttribute("aria-pressed"), "true");
    assert.match(harness.host.textContent, /99,6%/);
    assert.match(harness.host.textContent, /2\s*008 из 2\s*016/);
    assert.equal(
      harness.host.querySelectorAll('[aria-label*="проверок успешны"]').length,
      7,
      "the selected monitor exposes a daily seven-day breakdown",
    );
    const websiteRow = demoRows.find((button) =>
      button.getAttribute("aria-label").startsWith("Главная страница."),
    );
    assert.ok(websiteRow);
    await act(async () => websiteRow.click());
    await harness.flush();
    assert.equal(websiteRow.getAttribute("aria-pressed"), "true");
    assert.match(harness.host.textContent, /2\s*016 из 2\s*016/);

    await harness.click("У меня уже есть аккаунт");
    assert.equal(actions.login, 1);
    assert.equal(
      await harness.clickAll("Создать аккаунт"),
      3,
      "all public registration entry points share the current registration route",
    );
    assert.equal(actions.register, 3);
    await harness.click("Открыть раздел помощи");
    assert.equal(actions.help, 1);
  } finally {
    await harness.unmount();
  }
});

test("public price keeps the last confirmed value through refresh timeout, backgrounding, and a late response", async () => {
  const harness = await createHarness();
  const timers = fakePublicTimerRuntime();
  let reads = 0;
  let refreshSignal;
  let resolveRefresh;
  harness.setFetch(async (url, options) => {
    assert.equal(new URL(url).pathname, "/api/v1/billing/pricing");
    reads++;
    if (reads === 1)
      return jsonResponse({
        pricing: {
          pricePerMonitorMinor: 2400,
          currency: "RUB",
          period: "month",
          version: 4,
          updatedAt: "2026-10-27T08:00:00.000Z",
        },
      });
    if (reads === 2) {
      refreshSignal = options.signal;
      return new Promise((resolve) => {
        resolveRefresh = resolve;
      });
    }
    throw new Error("offline");
  });

  try {
    await harness.renderPublicScreen({ timerRuntime: timers.runtime });
    await harness.flush();
    assert.equal(reads, 1);
    assert.match(harness.host.textContent, /24\s*₽/);
    assert.doesNotMatch(harness.host.textContent, /Последняя полученная цена/);

    await act(async () => timers.advance(60_000));
    await harness.flush();
    assert.equal(
      reads,
      1,
      "a short foreground interval must not refetch pricing",
    );
    assert.match(harness.host.textContent, /24\s*₽/);

    await act(async () => timers.advance(4 * 60_000));
    await harness.flush();
    assert.equal(reads, 2, "the bounded freshness interval starts one refresh");
    assert.match(harness.host.textContent, /24\s*₽/);

    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("active"),
    );
    await harness.flush();
    assert.equal(
      reads,
      2,
      "overlapping foreground refreshes are single-flight",
    );

    await act(async () => timers.advance(5_000));
    await harness.flush();
    assert.equal(
      refreshSignal.aborted,
      true,
      "the GET is aborted at five seconds",
    );
    assert.match(harness.host.textContent, /24\s*₽/);
    assert.match(harness.host.textContent, /Последняя полученная цена/);
    assert.match(harness.host.textContent, /Обновить цену/);

    await act(async () =>
      resolveRefresh(
        jsonResponse({
          pricing: {
            pricePerMonitorMinor: 3500,
            currency: "RUB",
            period: "month",
            version: 5,
            updatedAt: "2026-10-27T08:05:00.000Z",
          },
        }),
      ),
    );
    await harness.flush();
    assert.match(harness.host.textContent, /24\s*₽/);
    assert.doesNotMatch(
      harness.host.textContent,
      /35\s*₽/,
      "a response arriving after timeout cannot replace the last confirmed value",
    );

    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("background"),
    );
    await harness.flush();
    assert.match(harness.host.textContent, /24\s*₽/);
    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("active"),
    );
    await harness.flush();
    assert.equal(reads, 3);
    assert.match(harness.host.textContent, /24\s*₽/);
    assert.match(harness.host.textContent, /Последняя полученная цена/);
  } finally {
    await harness.unmount();
  }
});

test("a fresh foreground read just before the periodic check prevents a duplicate GET", async () => {
  const harness = await createHarness();
  const timers = fakePublicTimerRuntime();
  let reads = 0;
  let resolveInitial;
  const quote = (minor, version) => ({
    pricing: {
      pricePerMonitorMinor: minor,
      currency: "RUB",
      period: "month",
      version,
      updatedAt: "2026-10-27T08:00:00.000Z",
    },
  });
  harness.setFetch(async () => {
    reads++;
    if (reads === 1)
      return new Promise((resolve) => {
        resolveInitial = resolve;
      });
    return jsonResponse(quote(3500, 5));
  });

  try {
    await harness.renderPublicScreen({ timerRuntime: timers.runtime });
    await harness.flush();
    assert.equal(reads, 1);
    await act(async () => timers.advance(4_000));
    await act(async () => resolveInitial(jsonResponse(quote(2400, 4))));
    await harness.flush();
    assert.match(harness.host.textContent, /24\s*₽/);

    await act(async () => timers.advance(296_000));
    await harness.flush();
    assert.equal(reads, 1, "60-second ticks keep a still-fresh quote quiet");
    await act(async () => timers.advance(5_000));
    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("background"),
    );
    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("active"),
    );
    await harness.flush();
    assert.equal(
      reads,
      2,
      "foreground refreshes the quote after it becomes stale",
    );
    assert.match(harness.host.textContent, /35\s*₽/);

    await act(async () => timers.advance(55_000));
    await harness.flush();
    assert.equal(
      reads,
      2,
      "the next periodic tick observes the successful foreground timestamp",
    );
    assert.match(harness.host.textContent, /35\s*₽/);
  } finally {
    await harness.unmount();
  }
});

test("public price aborts its pending read on unmount and ignores the late result", async () => {
  const harness = await createHarness();
  const timers = fakePublicTimerRuntime();
  let signal;
  let resolvePricing;
  harness.setFetch(async (_url, options) => {
    signal = options.signal;
    return new Promise((resolve) => {
      resolvePricing = resolve;
    });
  });

  await harness.renderPublicScreen({ timerRuntime: timers.runtime });
  await harness.flush();
  assert.equal(typeof resolvePricing, "function");
  assert.equal(
    timers.pendingCount(),
    2,
    "timeout and periodic refresh are scheduled",
  );

  await harness.unmount();
  assert.equal(signal.aborted, true);
  assert.equal(timers.pendingCount(), 0, "unmount clears both timers");
  resolvePricing(
    jsonResponse({
      pricing: {
        pricePerMonitorMinor: 2400,
        currency: "RUB",
        period: "month",
        version: 4,
        updatedAt: "2026-10-27T08:00:00.000Z",
      },
    }),
  );
  await Promise.resolve();
  await Promise.resolve();
});

test("a closed monitor incident opens its own scroll page and returns to the monitor", async () => {
  const harness = await createHarness();
  const monitor = mobileMonitor();
  const incident = {
    id: "20000000-0000-4000-8000-000000000041",
    number: 41,
    openedAt: "2026-10-20T10:00:00.000Z",
    confirmedAt: "2026-10-20T10:05:00.000Z",
    closedAt: "2026-10-20T10:15:00.000Z",
    closeReason: "recovered",
  };
  harness.native.restore = async () => ({
    account: { id: "account-incident-test" },
    session: {
      id: "session-incident-test",
      token: "incident-test-token",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  });
  await harness.apiModule.restoreSession();
  harness.setFetch(async (url) => {
    const path = new URL(url).pathname.replace("/api/v1", "");
    if (path === "/monitor-time-zones") return jsonResponse({ timeZones: [] });
    if (path === "/monitors") return jsonResponse(mobileMonitorPage([monitor]));
    if (path === "/monitors/monitor-1") return jsonResponse({ monitor });
    if (path === "/monitors/monitor-1/results")
      return jsonResponse({ results: [mobileResult("incident-result")] });
    if (path === "/monitors/monitor-1/incidents")
      return jsonResponse({ incidents: [incident] });
    throw new Error(`Unexpected monitor request: ${path}`);
  });

  try {
    await harness.renderMonitorsScreen();
    await harness.click("Primary monitor. Доступен");
    await harness.flush();
    await harness.click("Инциденты · 1");
    const monitorScrollPage = harness.host.querySelector(
      '[data-native-component="ScrollView"]',
    );
    assert.ok(monitorScrollPage, "the monitor detail has a scroll container");

    await harness.click("Подробнее");
    await harness.flush();
    const incidentScrollPage = harness.host.querySelector(
      '[data-native-component="ScrollView"]',
    );
    assert.notEqual(
      incidentScrollPage,
      monitorScrollPage,
      "the selected incident remounts a fresh scroll page at its start",
    );
    assert.match(harness.host.textContent, /Инцидент №41/);
    assert.match(harness.host.textContent, /Сервис восстановился/);
    assert.match(harness.host.textContent, /Завершён/);
    assert.match(harness.host.textContent, /20\.10\.2026/);

    await act(async () => harness.pressBack());
    await harness.flush();
    assert.match(harness.host.textContent, /Primary monitor/);
    assert.doesNotMatch(harness.host.textContent, /Инцидент №41/);
    assert.notEqual(
      harness.host.querySelector('[data-native-component="ScrollView"]'),
      incidentScrollPage,
      "Android Back closes incident details and restores the monitor screen",
    );

    await harness.click("Подробнее");
    await harness.flush();
    assert.match(harness.host.textContent, /Инцидент №41/);
    await harness.click("К монитору");
    await harness.flush();
    assert.match(harness.host.textContent, /Primary monitor/);
    assert.doesNotMatch(harness.host.textContent, /Инцидент №41/);
    assert.notEqual(
      harness.host.querySelector('[data-native-component="ScrollView"]'),
      incidentScrollPage,
      "returning also remounts the monitor page rather than keeping its old scroll position",
    );
  } finally {
    await harness.unmount();
  }
});

test("opening a resolved incident from the feed fetches its linked monitor and returns to the feed", async () => {
  const harness = await createHarness();
  const monitor = mobileMonitor();
  const incident = {
    id: "20000000-0000-4000-8000-000000000042",
    number: 42,
    openedAt: "2026-10-20T10:00:00.000Z",
    confirmedAt: "2026-10-20T10:05:00.000Z",
    closedAt: "2026-10-20T10:15:00.000Z",
    closeReason: "recovered",
  };
  const feedReads = [];
  const linkedReads = [];
  harness.native.restore = async () => ({
    account: { id: "account-feed-test" },
    session: {
      id: "session-feed-test",
      token: "feed-test-token",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  });
  await harness.apiModule.restoreSession();
  harness.setFetch(async (url) => {
    const parsed = new URL(url);
    const path = parsed.pathname.replace("/api/v1", "");
    if (path === "/monitor-time-zones") return jsonResponse({ timeZones: [] });
    if (path === "/monitors") return jsonResponse(mobileMonitorPage([monitor]));
    if (path === "/monitors/monitor-1") return jsonResponse({ monitor });
    if (path === "/monitors/monitor-1/results")
      return jsonResponse({ results: [mobileResult("feed-result")] });
    if (path === "/monitors/monitor-1/incidents")
      return jsonResponse({ incidents: [incident] });
    if (
      path ===
      "/monitors/monitor-1/incidents/20000000-0000-4000-8000-000000000042"
    ) {
      linkedReads.push(path);
      return jsonResponse({ incident });
    }
    if (path === "/incidents") {
      const state = parsed.searchParams.get("state");
      feedReads.push(state);
      return jsonResponse({
        incidents:
          state === "closed"
            ? [
                {
                  ...incident,
                  monitorId: monitor.id,
                  monitor: {
                    id: monitor.id,
                    name: monitor.name,
                    checkType: monitor.checkType,
                    target: monitor.url,
                    timeZone: monitor.timeZone,
                  },
                },
              ]
            : [],
        pagination: { nextCursor: null, hasMore: false },
        asOf: "2026-10-27T08:00:00.000Z",
      });
    }
    throw new Error(`Unexpected incident request: ${path}${parsed.search}`);
  });

  try {
    await harness.renderMonitorsScreen();
    await harness.click("Лента инцидентов");
    await harness.click("Завершённые");
    assert.deepEqual(feedReads, ["active", "closed"]);
    await harness.click("Инцидент 42, Primary monitor");
    await harness.flush();
    assert.deepEqual(linkedReads, [
      "/monitors/monitor-1/incidents/20000000-0000-4000-8000-000000000042",
    ]);
    assert.match(harness.host.textContent, /Инцидент №42/);
    assert.match(harness.host.textContent, /Primary monitor/);

    await harness.click("К ленте инцидентов");
    await harness.flush();
    assert.match(harness.host.textContent, /Инциденты/);
    assert.deepEqual(
      feedReads,
      ["active", "closed", "closed"],
      "returning from the incident restores the selected closed filter",
    );
    assert.ok(
      harness.host.querySelector(
        'button[aria-label="Инцидент 42, Primary monitor"]',
      ),
      "the resolved row is still visible after returning",
    );
    assert.ok(
      [...harness.host.querySelectorAll("button")].some((button) =>
        button.textContent.includes("К мониторам"),
      ),
      "Back returns to the incident feed instead of leaving the monitor detail underneath",
    );
  } finally {
    await harness.unmount();
  }
});

test("an incident deep link consumes its token once and opens the matching incident", async () => {
  const harness = await createHarness();
  const monitorId = "monitor-1";
  const incidentId = "20000000-0000-4000-8000-000000000043";
  const monitor = mobileMonitor({ id: monitorId });
  const incident = {
    id: incidentId,
    number: 43,
    openedAt: "2026-10-20T10:00:00.000Z",
    confirmedAt: "2026-10-20T10:05:00.000Z",
    closedAt: "2026-10-20T10:15:00.000Z",
    closeReason: "recovered",
  };
  const consumed = [];
  const incidentRequests = [];
  harness.native.restore = async () => ({
    account: { id: "account-deeplink-test" },
    session: {
      id: "session-deeplink-test",
      token: "deeplink-test-token",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  });
  await harness.apiModule.restoreSession();
  harness.setFetch(async (url) => {
    const path = new URL(url).pathname.replace("/api/v1", "");
    if (path === "/monitor-time-zones") return jsonResponse({ timeZones: [] });
    if (path === `/monitors/${monitorId}`) return jsonResponse({ monitor });
    if (path === `/monitors/${monitorId}/results`)
      return jsonResponse({ results: [mobileResult("deep-link-result")] });
    if (path === `/monitors/${monitorId}/incidents`)
      return jsonResponse({ incidents: [incident] });
    if (path === `/monitors/${monitorId}/incidents/${incidentId}`) {
      incidentRequests.push(path);
      return jsonResponse({ incident });
    }
    throw new Error(`Unexpected deep-link request: ${path}`);
  });

  try {
    await harness.renderMonitorsScreen(
      { monitorId, incidentId },
      { onDeepLinkConsumed: (link) => consumed.push(link) },
    );
    await harness.flush();
    assert.deepEqual(consumed, [{ monitorId, incidentId }]);
    assert.deepEqual(incidentRequests, [
      `/monitors/${monitorId}/incidents/${incidentId}`,
    ]);
    assert.match(harness.host.textContent, /Инцидент №43/);
    assert.match(harness.host.textContent, /Primary monitor/);
    await harness.click("К монитору");
    assert.match(harness.host.textContent, /Primary monitor/);
  } finally {
    await harness.unmount();
  }
});

test("closing incident details before monitor refresh resolves ignores the late refresh", async () => {
  const harness = await createHarness();
  const monitorId = "monitor-1";
  const incidentId = "20000000-0000-4000-8000-000000000044";
  const monitor = mobileMonitor({ id: monitorId });
  const incident = {
    id: incidentId,
    number: 44,
    openedAt: "2026-10-20T10:00:00.000Z",
    confirmedAt: "2026-10-20T10:05:00.000Z",
    closedAt: "2026-10-20T10:15:00.000Z",
    closeReason: "recovered",
  };
  let monitorReads = 0;
  let resolveLateMonitor;
  harness.native.restore = async () => ({
    account: { id: "account-close-race-test" },
    session: {
      id: "session-close-race-test",
      token: "close-race-test-token",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  });
  await harness.apiModule.restoreSession();
  harness.setFetch(async (url) => {
    const path = new URL(url).pathname.replace("/api/v1", "");
    if (path === "/monitor-time-zones") return jsonResponse({ timeZones: [] });
    if (path === "/monitors") return jsonResponse(mobileMonitorPage([monitor]));
    if (path === `/monitors/${monitorId}`) {
      monitorReads++;
      if (monitorReads === 1) return jsonResponse({ monitor });
      return new Promise((resolve) => {
        resolveLateMonitor = resolve;
      });
    }
    if (path === `/monitors/${monitorId}/results`)
      return jsonResponse({ results: [mobileResult("late-detail-result")] });
    if (path === `/monitors/${monitorId}/incidents`)
      return jsonResponse({ incidents: [incident] });
    if (path === `/monitors/${monitorId}/incidents/${incidentId}`)
      return jsonResponse({ incident });
    throw new Error(`Unexpected close-race request: ${path}`);
  });

  try {
    await harness.renderMonitorsScreen({ monitorId, incidentId });
    await harness.flush();
    assert.equal(typeof resolveLateMonitor, "function");
    assert.match(harness.host.textContent, /Инцидент №44/);

    await harness.click("К монитору");
    assert.match(harness.host.textContent, /Primary monitor/);
    assert.doesNotMatch(harness.host.textContent, /Инцидент №44/);
    await act(async () => resolveLateMonitor(jsonResponse({ monitor })));
    await harness.flush();
    assert.match(harness.host.textContent, /Primary monitor/);
    assert.doesNotMatch(
      harness.host.textContent,
      /Инцидент №44/,
      "a late detail response must not reopen the incident after the user closes it",
    );
  } finally {
    await harness.unmount();
  }
});

test("shared mobile brand follows reduced-motion and app-state changes, with light and dark palettes", async () => {
  const harness = await createHarness({ reducedMotion: true });
  try {
    await harness.renderPage(async () => {});
    assert.equal(harness.nativeMocks.intervalCounts.started, 0);
    assert.equal(
      harness.host.querySelector("section").style.backgroundColor,
      "rgb(245, 249, 248)",
    );
    harness.nativeMocks.setColorScheme("dark");
    await harness.renderPage(async () => {});
    assert.equal(
      harness.host.querySelector("section").style.backgroundColor,
      "rgb(16, 29, 32)",
    );

    await act(async () =>
      harness.nativeMocks.AccessibilityInfo.setReducedMotion(false),
    );
    await harness.flush();
    assert.equal(harness.nativeMocks.intervalCounts.started, 1);
    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("background"),
    );
    await harness.flush();
    assert.equal(harness.nativeMocks.intervalCounts.cleared, 1);
    assert.equal([...harness.nativeMocks.animatedValues][0].value, 0);
    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("active"),
    );
    await harness.flush();
    assert.equal(harness.nativeMocks.intervalCounts.started, 2);
    await act(async () =>
      harness.nativeMocks.AccessibilityInfo.setReducedMotion(true),
    );
    await harness.flush();
    assert.equal(harness.nativeMocks.intervalCounts.cleared, 2);
    assert.equal([...harness.nativeMocks.animatedValues][0].value, 0);
  } finally {
    await harness.unmount();
  }
});

test("mobile motion stays off after a settings read failure", async () => {
  const harness = await createHarness({
    motionRead: Promise.reject(new Error("settings unavailable")),
  });
  try {
    harness.nativeMocks.setManualAnimations(true);
    await harness.renderPublicScreen();
    const website = [
      ...harness.host.querySelectorAll("button[aria-label]"),
    ].find((button) =>
      button.getAttribute("aria-label").startsWith("Главная страница."),
    );
    assert.ok(website, "the public demo monitor can still be selected");
    await act(async () => website.click());
    await harness.flush();
    assert.equal(
      harness.nativeMocks.animationLog.started,
      0,
      "an unavailable reduced-motion preference must default to no animation",
    );
    assert.equal(website.getAttribute("aria-pressed"), "true");
  } finally {
    await harness.unmount();
  }
});

test("a reduced-motion event wins over a late initial settings read", async () => {
  let resolveMotionRead;
  const motionRead = new Promise((resolve) => {
    resolveMotionRead = resolve;
  });
  const harness = await createHarness({ motionRead });
  try {
    harness.nativeMocks.setManualAnimations(true);
    await harness.renderPublicScreen();
    const findMonitorButton = (name) =>
      [...harness.host.querySelectorAll("button[aria-label]")].find((button) =>
        button.getAttribute("aria-label").startsWith(`${name}.`),
      );
    const monitorButtons = [
      ...harness.host.querySelectorAll("button[aria-label]"),
    ].filter((button) =>
      button.getAttribute("aria-label").includes("Успешные проверки за 7 дней"),
    );
    assert.equal(monitorButtons.length, 3);
    await act(async () => findMonitorButton("Главная страница")?.click());
    await harness.flush();
    assert.equal(harness.nativeMocks.animationLog.started, 0);

    await act(async () =>
      harness.nativeMocks.AccessibilityInfo.setReducedMotion(true),
    );
    await harness.flush();
    await act(async () => findMonitorButton("SSH сервера")?.click());
    await harness.flush();
    assert.equal(harness.nativeMocks.animationLog.started, 0);

    await act(async () => resolveMotionRead(false));
    await harness.flush();
    await act(async () => findMonitorButton("API магазина")?.click());
    await harness.flush();
    assert.equal(harness.nativeMocks.animationLog.started, 0);
    const selected = [
      ...harness.host.querySelectorAll("button[aria-label]"),
    ].find((button) => button.getAttribute("aria-pressed") === "true");
    assert.match(selected?.getAttribute("aria-label") ?? "", /^API магазина\./);
  } finally {
    await harness.unmount();
  }
});

test("monitor chart motion stops in background and does not replay on foreground", async () => {
  const harness = await createHarness({ reducedMotion: false });
  try {
    harness.nativeMocks.setManualAnimations(true);
    await harness.renderPublicScreen();
    await act(async () =>
      harness.nativeMocks.AccessibilityInfo.setReducedMotion(false),
    );
    await harness.flush();
    const monitorButtons = [
      ...harness.host.querySelectorAll("button[aria-label]"),
    ].filter((button) =>
      button.getAttribute("aria-label").includes("Успешные проверки за 7 дней"),
    );
    await act(async () => monitorButtons[0].click());
    await harness.flush();
    assert.equal(harness.nativeMocks.animationLog.started, 1);
    assert.equal(
      harness.nativeMocks.animationLog.handles[0].config.duration,
      180,
    );

    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("background"),
    );
    await harness.flush();
    assert.equal(harness.nativeMocks.animationLog.handles[0].active, false);
    const afterBackground = harness.nativeMocks.animationLog.started;
    assert.ok(harness.nativeMocks.animationLog.stopped >= 1);

    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("active"),
    );
    await harness.flush();
    assert.equal(
      harness.nativeMocks.animationLog.started,
      afterBackground,
      "returning to the foreground does not replay the selection animation",
    );
    await act(async () =>
      harness.nativeMocks.AccessibilityInfo.setReducedMotion(true),
    );
    await harness.flush();
    await act(async () =>
      harness.nativeMocks.AccessibilityInfo.setReducedMotion(false),
    );
    await harness.flush();
    assert.equal(
      harness.nativeMocks.animationLog.started,
      afterBackground,
      "loading a later preference change does not replay the same selection",
    );
  } finally {
    await harness.unmount();
  }
});

test("incident entry motion is cancelled by Android Back without changing navigation", async () => {
  const harness = await createHarness({ reducedMotion: false });
  const monitor = mobileMonitor();
  const incident = {
    id: "20000000-0000-4000-8000-000000000041",
    number: 41,
    status: "resolved",
    startedAt: "2026-10-20T10:00:00Z",
    resolvedAt: "2026-10-20T10:30:00Z",
    startedAtText: "20.10.2026 10:00",
    resolvedAtText: "20.10.2026 10:30",
    durationSeconds: 1800,
    summary: "Сервис восстановился",
    latestResultId: "incident-result",
  };
  harness.setFetch(async (url) => {
    const path = new URL(url).pathname.replace("/api/v1", "");
    if (path === "/monitor-time-zones") return jsonResponse({ timeZones: [] });
    if (path === "/monitors") return jsonResponse(mobileMonitorPage([monitor]));
    if (path === "/monitors/monitor-1") return jsonResponse({ monitor });
    if (path === "/monitors/monitor-1/results")
      return jsonResponse({ results: [mobileResult("incident-result")] });
    if (path === "/monitors/monitor-1/incidents")
      return jsonResponse({ incidents: [incident] });
    throw new Error(`Unexpected monitor request: ${path}`);
  });
  try {
    harness.native.restore = async () => ({
      account: { id: "account-motion-test" },
      session: {
        id: "session-motion-test",
        token: "motion-test-token",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    harness.nativeMocks.setManualAnimations(true);
    await harness.renderMonitorsScreen();
    await harness.click("Primary monitor. Доступен");
    await harness.flush();
    for (const handle of harness.nativeMocks.animationLog.handles)
      handle.finish();
    await harness.click("Инциденты · 1");
    await harness.flush();
    for (const handle of harness.nativeMocks.animationLog.handles)
      handle.finish();

    const startsBeforeEntry = harness.nativeMocks.animationLog.started;
    await harness.click("Подробнее");
    await harness.flush();
    assert.match(harness.host.textContent, /Инцидент №41/);
    assert.equal(
      harness.nativeMocks.animationLog.started,
      startsBeforeEntry + 1,
    );
    const entry = harness.nativeMocks.animationLog.handles.at(-1);
    assert.equal(entry.config.duration, 180);
    assert.equal(entry.active, true);

    await act(async () => harness.pressBack());
    await harness.flush();
    assert.match(harness.host.textContent, /Primary monitor/);
    assert.doesNotMatch(harness.host.textContent, /Инцидент №41/);
    assert.equal(
      entry.active,
      false,
      "Back cancels the interrupted detail animation",
    );
    await act(async () => entry.finish());
    assert.doesNotMatch(harness.host.textContent, /Инцидент №41/);
  } finally {
    await harness.unmount();
  }
});

test("shared button press animation is brief and keeps its callback immediate", async () => {
  const harness = await createHarness({ reducedMotion: false });
  let registrations = 0;
  try {
    harness.nativeMocks.setManualAnimations(true);
    await harness.renderPublicScreen({ onRegister: () => registrations++ });
    await act(async () =>
      harness.nativeMocks.AccessibilityInfo.setReducedMotion(false),
    );
    await harness.flush();
    const label = "Создать аккаунт";
    await harness.mouse(label, "mousedown");
    assert.equal(harness.nativeMocks.animationLog.pressIns, 1);
    assert.equal(
      harness.nativeMocks.animationLog.handles.at(-1).config.duration,
      80,
    );
    await harness.mouse(label, "mouseup");
    assert.equal(
      harness.nativeMocks.animationLog.handles.at(-1).config.duration,
      120,
    );
    const registrationButton = harness.byRole("button", label, {
      exactlyOne: false,
    });
    await act(async () => registrationButton.click());
    assert.equal(
      registrations,
      1,
      "the navigation callback does not wait for animation completion",
    );
    for (const handle of harness.nativeMocks.animationLog.handles)
      handle.finish();
    assert.equal(
      harness.nativeMocks.animationLog.handles.filter((handle) => handle.active)
        .length,
      0,
      "the pressed state settles after the response animation",
    );
  } finally {
    await harness.unmount();
  }
});

test("API errors distinguish transport, backend, and native bridge failures", async () => {
  const harness = await createHarness();
  try {
    harness.setFetch(async () => {
      throw new Error("offline");
    });
    await assert.rejects(
      harness.apiModule.api("/auth/native/login", { auth: false }),
      (error) => {
        assert.equal(
          harness.apiModule.errorText(error),
          "Не удалось подключиться. Проверьте сеть и повторите.",
        );
        return true;
      },
    );
    harness.setFetch(async () => ({
      ok: false,
      status: 401,
      headers: { get: () => "application/json" },
      json: async () => ({ error: "invalid_credentials" }),
    }));
    await assert.rejects(
      harness.apiModule.api("/auth/native/login", { auth: false }),
      (error) => {
        assert.equal(
          harness.apiModule.errorText(error),
          "Неверный email или пароль.",
        );
        return true;
      },
    );
    harness.native.authEpoch = async () => {
      throw new Error("native bridge rejected");
    };
    await assert.rejects(
      harness.apiModule.api("/auth/native/login", { auth: false }),
      (error) => {
        assert.equal(
          harness.apiModule.errorText(error),
          "Ошибка приложения. Перезапустите его и повторите действие.",
        );
        return true;
      },
    );
  } finally {
    await harness.unmount();
  }
});

test("authenticated invalid-credentials 401 keeps the session, while other 401s expire it", async () => {
  const harness = await createHarness();
  try {
    const validSession = {
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    };
    harness.native.restore = async () => validSession;
    await harness.apiModule.restoreSession();
    let expired = 0;
    harness.apiModule.onSessionExpired(() => expired++);
    harness.setFetch(async () => ({
      ok: false,
      status: 401,
      headers: { get: () => "application/json" },
      json: async () => ({ error: "invalid_credentials" }),
    }));

    await assert.rejects(
      harness.apiModule.api("/auth/native/password/change", { method: "POST" }),
      (error) => error.code === "invalid_credentials",
    );
    assert.equal(harness.apiModule.session()?.session.id, "session-1");
    assert.equal(harness.native.logoutCalls, 0);
    assert.equal(expired, 0);

    harness.setFetch(async () => ({
      ok: false,
      status: 401,
      headers: { get: () => "application/json" },
      json: async () => ({ error: "unauthorized" }),
    }));
    await assert.rejects(
      harness.apiModule.api("/me"),
      (error) => error.code === "session_expired",
    );
    assert.equal(harness.apiModule.session(), null);
    assert.equal(harness.native.logoutCalls, 1);
    assert.equal(expired, 1);
  } finally {
    await harness.unmount();
  }
});

test("aborted authenticated API requests stay aborts instead of network errors", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    const controller = new AbortController();
    harness.setFetch(async () => {
      const error = new Error("aborted");
      error.name = "AbortError";
      controller.abort();
      throw error;
    });
    await assert.rejects(
      harness.apiModule.api("/monitors?q=old", { signal: controller.signal }),
      (error) => {
        assert.equal(error.name, "AbortError");
        assert.notEqual(
          harness.apiModule.errorText(error),
          "Не удалось подключиться. Проверьте сеть и повторите.",
        );
        return true;
      },
    );
  } finally {
    await harness.unmount();
  }
});

test("page refresh shares one pending promise and keeps its control busy until it settles", async () => {
  const harness = await createHarness();
  try {
    let finish;
    let calls = 0;
    const pending = new Promise((resolve) => {
      finish = resolve;
    });
    await harness.renderPage(() => {
      calls++;
      return pending;
    });
    const refresh = harness.host.querySelector(
      'button[aria-label="Обновить страницу"]',
    );
    assert.ok(
      refresh,
      "pull-to-refresh is represented by an actionable control",
    );
    await act(async () => {
      refresh.click();
      refresh.click();
      await Promise.resolve();
    });
    assert.equal(calls, 1, "overlapping gestures share the in-flight refresh");
    assert.equal(refresh.dataset.refreshing, "true");
    await act(async () => {
      finish();
      await pending;
    });
    await act(async () => Promise.resolve());
    assert.equal(refresh.dataset.refreshing, "false");
  } finally {
    await harness.unmount();
  }
});

test("account refresh isolates a failed source and preserves a dirty profile name", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    let profileReads = 0;
    let failSessions = false;
    harness.setFetch(async (url) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/me") {
        profileReads++;
        return {
          ok: true,
          status: 200,
          headers: { get: () => "application/json" },
          json: async () => ({
            account: {
              id: "account-1",
              name: profileReads === 1 ? "Первое имя" : "Имя с сервера",
              email: "person@example.test",
              role: "user",
            },
            email: "person@example.test",
            hasPassword: true,
            emailVerified: true,
          }),
        };
      }
      if (path === "/me/sessions") {
        return {
          ok: !failSessions,
          status: failSessions ? 503 : 200,
          headers: { get: () => "application/json" },
          json: async () =>
            failSessions
              ? { error: "temporarily_unavailable" }
              : { sessions: [] },
        };
      }
      if (path === "/me/identities")
        return {
          ok: true,
          status: 200,
          headers: { get: () => "application/json" },
          json: async () => ({ identities: [] }),
        };
      throw new Error(`Unexpected account request: ${path}`);
    });
    await harness.renderAccountScreen();
    await harness.click("Профиль и email");
    const name = harness.host.querySelector('input[aria-label="Имя"]');
    assert.ok(
      name,
      "verified profile is visible after its own successful read",
    );
    assert.equal(name.value, "Первое имя");
    await harness.fill("Имя", "Локальный черновик");

    failSessions = true;
    await act(async () =>
      harness.host
        .querySelector('button[aria-label="Обновить страницу"]')
        .click(),
    );
    await harness.flush();
    assert.equal(
      profileReads,
      2,
      "profile refreshed independently of sessions",
    );
    assert.equal(
      harness.host.querySelector('input[aria-label="Имя"]').value,
      "Локальный черновик",
      "a server refresh must not overwrite an unsaved edit",
    );
    await harness.click("К аккаунту");
    await harness.click("Безопасность и сеансы");
    assert.match(harness.host.textContent, /Способы входа/);
    assert.match(
      harness.host.textContent,
      /сервис временно недоступен/i,
      "a failed sessions source is reported in its security subsection",
    );
    await harness.click("К аккаунту");
    await harness.click("Профиль и email");
    assert.equal(
      harness.host.querySelector('input[aria-label="Имя"]').value,
      "Локальный черновик",
      "the healthy profile section remains available with its unsaved edit",
    );
  } finally {
    await harness.unmount();
  }
});

test("account hub opens its profile and security subsections and returns to the hub", async () => {
  const harness = await createHarness();
  try {
    await harness.renderAccountScreen();
    harness.byRole("header", "Аккаунт");

    await harness.click("Профиль и email");
    harness.byRole("header", "Профиль", { exactlyOne: false });
    await harness.click("К аккаунту");
    harness.byRole("header", "Аккаунт");

    await harness.click("Безопасность и сеансы");
    harness.byRole("header", "Безопасность");
    await harness.click("К аккаунту");
    harness.byRole("header", "Аккаунт");

    await harness.click("Помощь и документы");
    harness.byRole("header", "Помощь и документы");
    await harness.click("Назад");
    harness.byRole("header", "Аккаунт");
  } finally {
    await harness.unmount();
  }
});

test("account does not offer linked providers until identity state is verified", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    let resolveIdentities;
    const identitiesPending = new Promise((resolve) => {
      resolveIdentities = resolve;
    });
    harness.setFetch(async (url) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/me")
        return jsonResponse({
          account: {
            id: "account-1",
            name: "Имя",
            email: "person@example.test",
            role: "user",
          },
          email: "person@example.test",
          hasPassword: true,
          emailVerified: true,
        });
      if (path === "/me/sessions") return jsonResponse({ sessions: [] });
      if (path === "/me/identities") return identitiesPending;
      throw new Error(`Unexpected account request: ${path}`);
    });
    await harness.renderAccountScreen();
    await harness.click("Безопасность и сеансы");
    assert.equal(
      harness.host.querySelector('button[aria-label="Подключить Яндекс"]'),
      null,
      "an unknown/loading identity list cannot expose a new link action",
    );
    await act(async () =>
      resolveIdentities(
        jsonResponse({
          identities: [{ id: "linked-yandex", provider: "yandex" }],
        }),
      ),
    );
    await harness.flush();
    assert.match(harness.host.textContent, /Яндекс/);
    const buttonFor = (text) =>
      [...harness.host.querySelectorAll("button")].find((button) =>
        button.textContent.includes(text),
      );
    assert.equal(
      !!buttonFor("Подключить Яндекс"),
      false,
      "a provider with a linked identity is shown as linked, not connectable",
    );
    assert.ok(
      buttonFor("Подключить VK"),
      "a different verified, unlinked provider remains available",
    );
  } finally {
    await harness.unmount();
  }
});

test("push diagnostics appear only after the current account is verified as admin", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    harness.native.pushStatus = async () => ({
      enabled: true,
      optedOut: false,
      provider: "push-gateway-test",
      status: "ready",
    });
    let resolveFirstProfile;
    let nextProfile;
    const firstProfile = new Promise((resolve) => {
      resolveFirstProfile = resolve;
    });
    harness.setFetch(async (url) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/me") return nextProfile ?? firstProfile;
      if (path === "/me/sessions") return jsonResponse({ sessions: [] });
      if (path === "/me/identities") return jsonResponse({ identities: [] });
      throw new Error(`Unexpected account request: ${path}`);
    });
    await harness.renderAccountScreen();
    await harness.click("Уведомления");
    assert.doesNotMatch(
      harness.host.textContent,
      /push-gateway-test|Провайдер:/,
      "push metadata must not flash before profile authorization is checked",
    );
    await act(async () =>
      resolveFirstProfile(
        jsonResponse({
          account: {
            id: "other-account",
            name: "Старый профиль",
            email: "old@example.test",
            role: "admin",
          },
          email: "old@example.test",
          hasPassword: true,
          emailVerified: true,
        }),
      ),
    );
    await harness.flush();
    assert.doesNotMatch(
      harness.host.textContent,
      /push-gateway-test|Провайдер:/,
      "an admin role from another account is not sufficient",
    );

    let resolveCurrentProfile;
    nextProfile = new Promise((resolve) => {
      resolveCurrentProfile = resolve;
    });
    await act(async () =>
      harness.host
        .querySelector('button[aria-label="Обновить страницу"]')
        .click(),
    );
    await harness.flush();
    assert.doesNotMatch(
      harness.host.textContent,
      /push-gateway-test|Провайдер:/,
      "refresh hides old privilege details while the new profile is loading",
    );
    await act(async () =>
      resolveCurrentProfile(
        jsonResponse({
          account: {
            id: "account-1",
            name: "Текущий администратор",
            email: "admin@example.test",
            role: "admin",
          },
          email: "admin@example.test",
          hasPassword: true,
          emailVerified: true,
        }),
      ),
    );
    await harness.flush();
    assert.match(
      harness.host.textContent,
      /Провайдер: push-gateway-test · ready/,
    );
    await harness.click("К аккаунту");
    await harness.click("Безопасность и сеансы");
    assert.match(
      harness.host.textContent,
      /Администратор управляет способами входа/,
    );
    assert.equal(
      [...harness.host.querySelectorAll("button")].some((button) =>
        button.textContent.includes("Открыть настройки на сайте"),
      ),
      false,
      "admin help must not offer the web settings route blocked by native navigation policy",
    );
  } finally {
    await harness.unmount();
  }
});

test("raw monitor search aborts stale work before debounce without showing a network error", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    let monitorListCalls = 0;
    let pendingList;
    let pendingResponse;
    harness.setFetch(async (url, options) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors") {
        monitorListCalls++;
        if (monitorListCalls === 1)
          return jsonResponse(mobileMonitorPage([mobileMonitor()]));
        if (monitorListCalls === 2) {
          pendingList = options;
          return new Promise((resolve) => {
            pendingResponse = resolve;
          });
        }
        return jsonResponse(
          mobileMonitorPage([
            mobileMonitor({ id: "monitor-filtered", name: "Filtered result" }),
          ]),
        );
      }
      throw new Error(`Unexpected monitor request: ${path}`);
    });
    await harness.renderMonitorsScreen();
    assert.match(harness.host.textContent, /Primary monitor/);
    await act(async () =>
      harness.host
        .querySelector('button[aria-label="Обновить страницу"]')
        .click(),
    );
    await harness.flush();
    assert.ok(
      pendingList?.signal,
      "refresh passes an abort signal to list GET",
    );

    await harness.fill("Поиск мониторов", "Filtered");
    assert.equal(
      pendingList.signal.aborted,
      true,
      "new raw input invalidates the old request before the 300 ms search delay",
    );
    await act(async () => {
      pendingResponse(
        jsonResponse(
          mobileMonitorPage([
            mobileMonitor({ id: "stale", name: "Stale refresh response" }),
          ]),
        ),
      );
    });
    await harness.flush();
    assert.match(harness.host.textContent, /Primary monitor/);
    assert.doesNotMatch(harness.host.textContent, /Stale refresh response/);
    assert.doesNotMatch(
      harness.host.textContent,
      /Не удалось подключиться/,
      "an intentional abort is not presented as a network outage",
    );

    await act(async () => new Promise((resolve) => setTimeout(resolve, 350)));
    await harness.flush();
    assert.match(harness.host.textContent, /Filtered result/);
    assert.equal(monitorListCalls, 3);
  } finally {
    await harness.unmount();
  }
});

test("monitor detail refresh keeps old content visible and applies the refreshed state", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    let detailReads = 0;
    let resolveMonitor;
    harness.setFetch(async (url) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors")
        return jsonResponse(mobileMonitorPage([mobileMonitor()]));
      if (path === "/monitors/monitor-1") {
        detailReads++;
        if (detailReads === 1)
          return jsonResponse({ monitor: mobileMonitor() });
        return new Promise((resolve) => {
          resolveMonitor = resolve;
        });
      }
      if (path === "/monitors/monitor-1/results")
        return jsonResponse({
          results: [
            mobileResult(
              `detail-${detailReads}`,
              detailReads === 1 ? "request_failed" : "request_timeout",
            ),
          ],
        });
      if (path === "/monitors/monitor-1/incidents")
        return jsonResponse({ incidents: [] });
      throw new Error(`Unexpected monitor request: ${path}`);
    });
    await harness.renderMonitorsScreen();
    await harness.click("Primary monitor. Доступен");
    await harness.flush();
    assert.match(harness.host.textContent, /Доступен/);
    assert.match(harness.host.textContent, /Ошибка соединения/);

    await act(async () =>
      harness.host
        .querySelector('button[aria-label="Обновить страницу"]')
        .click(),
    );
    await harness.flush();
    assert.match(harness.host.textContent, /Доступен/);
    assert.match(harness.host.textContent, /Ошибка соединения/);
    await act(async () =>
      resolveMonitor(
        jsonResponse({ monitor: mobileMonitor({ state: "DOWN" }) }),
      ),
    );
    await harness.flush();
    assert.match(harness.host.textContent, /Недоступен/);
    assert.match(harness.host.textContent, /Превышено время ожидания/);
  } finally {
    await harness.unmount();
  }
});

test("collapsing monitor history fences a pending older-results page", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    const firstResults = Array.from({ length: 50 }, (_, index) =>
      mobileResult(`first-${index}`, "request_failed"),
    );
    let olderResponse;
    let olderPageCalls = 0;
    harness.setFetch(async (url) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors")
        return jsonResponse(mobileMonitorPage([mobileMonitor()]));
      if (path === "/monitors/monitor-1")
        return jsonResponse({ monitor: mobileMonitor() });
      if (path === "/monitors/monitor-1/results") {
        if (parsed.searchParams.has("before")) {
          olderPageCalls++;
          if (olderPageCalls === 1)
            return jsonResponse({
              results: Array.from({ length: 50 }, (_, index) =>
                mobileResult(`older-${index}`, "request_timeout"),
              ),
            });
          return new Promise((resolve) => {
            olderResponse = resolve;
          });
        }
        return jsonResponse({ results: firstResults });
      }
      if (path === "/monitors/monitor-1/incidents")
        return jsonResponse({ incidents: [] });
      throw new Error(`Unexpected monitor request: ${path}`);
    });
    await harness.renderMonitorsScreen();
    await harness.click("Primary monitor. Доступен");
    await harness.flush();
    assert.match(harness.host.textContent, /Ошибка соединения/);
    await harness.click("История проверок · 50");
    await act(async () =>
      [...harness.host.querySelectorAll("button")]
        .find((button) =>
          button.textContent.includes("Более ранние результаты"),
        )
        .click(),
    );
    await harness.flush();
    assert.match(harness.host.textContent, /Превышено время ожидания/);
    await act(async () =>
      [...harness.host.querySelectorAll("button")]
        .find((button) =>
          button.textContent.includes("Более ранние результаты"),
        )
        .click(),
    );
    await harness.flush();
    assert.equal(typeof olderResponse, "function");
    await harness.click("Свернуть историю");
    await act(async () => {
      olderResponse({
        ok: true,
        status: 200,
        headers: { get: () => "application/json" },
        json: async () => ({
          results: [mobileResult("late-older", "unexpected_status")],
        }),
      });
    });
    await harness.flush();
    assert.match(harness.host.textContent, /Ошибка соединения/);
    assert.doesNotMatch(
      harness.host.textContent,
      /Неожиданный HTTP-код/,
      "a late paging response cannot reappend history after collapse",
    );
    assert.doesNotMatch(
      harness.host.textContent,
      /Превышено время ожидания/,
      "collapsing history removes the previously loaded older page",
    );
    assert.equal(
      [...harness.host.querySelectorAll("button")].some(
        (button) => button.textContent.trim() === "Свернуть историю",
      ),
      false,
      "the exact collapse control disappears after history is collapsed",
    );
    assert.equal(
      [...harness.host.querySelectorAll("button")].some(
        (button) => button.textContent.trim() === "Свернуть историю",
      ),
      false,
      "the exact collapse control is removed after collapsing the older page",
    );
    assert.equal(
      (harness.host.textContent.match(/Ошибка соединения/g) ?? []).length,
      50,
      "the original 50 visible results remain after collapsing the older page",
    );
  } finally {
    await harness.unmount();
  }
});

test("editing a monitor keeps its opened version when a newer detail read finishes late", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    let detailReads = 0;
    let resolveLateDetail;
    let patchBody;
    harness.setFetch(async (url, options) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors")
        return jsonResponse(mobileMonitorPage([mobileMonitor({ version: 7 })]));
      if (path === "/monitors/monitor-1" && options.method === "PATCH") {
        patchBody = JSON.parse(options.body);
        return jsonResponse({ error: "version_conflict" }, 409);
      }
      if (path === "/monitors/monitor-1") {
        detailReads++;
        if (detailReads === 1)
          return jsonResponse({ monitor: mobileMonitor({ version: 7 }) });
        return new Promise((resolve) => {
          resolveLateDetail = resolve;
        });
      }
      if (path === "/monitors/monitor-1/results")
        return jsonResponse({ results: [mobileResult("edit-result")] });
      if (path === "/monitors/monitor-1/incidents")
        return jsonResponse({ incidents: [] });
      throw new Error(`Unexpected monitor request: ${path}`);
    });
    await harness.renderMonitorsScreen();
    await harness.click("Primary monitor. Доступен");
    await harness.flush();
    await act(async () =>
      harness.host
        .querySelector('button[aria-label="Обновить страницу"]')
        .click(),
    );
    await harness.flush();
    assert.equal(typeof resolveLateDetail, "function");
    await act(async () =>
      [...harness.host.querySelectorAll("button")]
        .find((button) => button.textContent.includes("Изменить"))
        .click(),
    );
    assert.ok(harness.host.querySelector('input[aria-label="Название"]'));
    await act(async () =>
      resolveLateDetail(
        jsonResponse({
          monitor: mobileMonitor({ version: 8, name: "Newer name" }),
        }),
      ),
    );
    await harness.flush();
    assert.ok(harness.host.querySelector('input[aria-label="Название"]'));
    assert.equal(
      harness.host.querySelector('input[aria-label="Название"]').value,
      "Primary monitor",
    );
    await harness.fill("Название", "My edit");
    await act(async () =>
      [...harness.host.querySelectorAll("button")]
        .find((button) => button.textContent.includes("Сохранить"))
        .click(),
    );
    await harness.flush();
    assert.equal(patchBody?.version, 7);
    assert.match(harness.host.textContent, /Данные изменились/);
  } finally {
    await harness.unmount();
  }
});

test("detail refresh fences old pagination, blocks new paging, and refreshes the cursor", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    const firstPage = Array.from({ length: 50 }, (_, index) =>
      mobileResult(
        `initial-${index}`,
        "request_failed",
        `2026-10-21T12:${String(index).padStart(2, "0")}:00Z`,
      ),
    );
    const refreshedPage = Array.from({ length: 50 }, (_, index) =>
      mobileResult(
        `refreshed-${index}`,
        "request_timeout",
        `2026-10-22T12:${String(index).padStart(2, "0")}:00Z`,
      ),
    );
    let detailReads = 0;
    let resultsReads = 0;
    let oldPageResponse;
    let refreshedResultsResponse;
    let pageCursors = [];
    harness.setFetch(async (url) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors")
        return jsonResponse(mobileMonitorPage([mobileMonitor()]));
      if (path === "/monitors/monitor-1") {
        detailReads++;
        return jsonResponse({
          monitor: mobileMonitor({ version: detailReads }),
        });
      }
      if (path === "/monitors/monitor-1/results") {
        if (parsed.searchParams.has("before")) {
          pageCursors.push(parsed.searchParams.get("before"));
          if (pageCursors.length === 1)
            return new Promise((resolve) => {
              oldPageResponse = resolve;
            });
          return jsonResponse({
            results: [mobileResult("fresh-older", "unexpected_status")],
          });
        }
        resultsReads++;
        if (resultsReads === 1) return jsonResponse({ results: firstPage });
        return new Promise((resolve) => {
          refreshedResultsResponse = resolve;
        });
      }
      if (path === "/monitors/monitor-1/incidents")
        return jsonResponse({ incidents: [] });
      throw new Error(`Unexpected monitor request: ${path}`);
    });
    await harness.renderMonitorsScreen();
    await harness.click("Primary monitor. Доступен");
    await harness.flush();
    await harness.click("История проверок · 50");
    await act(async () =>
      [...harness.host.querySelectorAll("button")]
        .find((button) =>
          button.textContent.includes("Более ранние результаты"),
        )
        .click(),
    );
    await harness.flush();
    assert.equal(typeof oldPageResponse, "function");

    await act(async () =>
      harness.host
        .querySelector('button[aria-label="Обновить страницу"]')
        .click(),
    );
    await harness.flush();
    assert.equal(typeof refreshedResultsResponse, "function");
    await act(async () =>
      [...harness.host.querySelectorAll("button")]
        .find((button) =>
          button.textContent.includes("Более ранние результаты"),
        )
        .click(),
    );
    await harness.flush();
    assert.equal(
      pageCursors.length,
      1,
      "paging cannot start during a detail refresh",
    );

    await act(async () =>
      oldPageResponse(
        jsonResponse({
          results: [mobileResult("stale-older", "tls_certificate_invalid")],
        }),
      ),
    );
    await harness.flush();
    assert.doesNotMatch(harness.host.textContent, /Ошибка сертификата TLS/);
    await act(async () =>
      refreshedResultsResponse(jsonResponse({ results: refreshedPage })),
    );
    await harness.flush();
    assert.match(harness.host.textContent, /Превышено время ожидания/);
    assert.doesNotMatch(harness.host.textContent, /Ошибка соединения/);

    await act(async () =>
      [...harness.host.querySelectorAll("button")]
        .find((button) =>
          button.textContent.includes("Более ранние результаты"),
        )
        .click(),
    );
    await harness.flush();
    assert.equal(pageCursors.length, 2);
    assert.equal(pageCursors[1], refreshedPage.at(-1).checkedAt);
    assert.match(harness.host.textContent, /Неожиданный HTTP-код/);
    assert.doesNotMatch(harness.host.textContent, /Ошибка сертификата TLS/);
  } finally {
    await harness.unmount();
  }
});

test("account action retries clear only their own card error", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    let passwordAttempts = 0;
    let resolvePasswordRetry;
    harness.native.beginOAuth = async () => ({
      state: "oauth-state",
      codeChallenge: "oauth-challenge",
    });
    harness.setFetch(async (url, options) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/me")
        return jsonResponse({
          account: {
            id: "account-1",
            name: "Имя",
            email: "person@example.test",
            role: "user",
          },
          email: "person@example.test",
          hasPassword: true,
          emailVerified: true,
        });
      if (path === "/me/sessions") return jsonResponse({ sessions: [] });
      if (path === "/me/identities") return jsonResponse({ identities: [] });
      if (path === "/auth/native/oauth/yandex/start")
        return jsonResponse({ error: "temporarily_unavailable" }, 503);
      if (
        path === "/auth/native/password/change" &&
        options.method === "POST"
      ) {
        passwordAttempts++;
        if (passwordAttempts === 1)
          return jsonResponse({ error: "invalid_credentials" }, 401);
        return new Promise((resolve) => {
          resolvePasswordRetry = resolve;
        });
      }
      throw new Error(`Unexpected account request: ${path}`);
    });
    await harness.renderAccountScreen();
    await harness.click("Безопасность и сеансы");
    await harness.fill("Текущий пароль", "incorrect");
    await harness.fill("Новый пароль", "new-password-strong");
    await harness.click("Изменить пароль");
    await harness.flush();

    const cardAlert = (headingText) => {
      const heading = [
        ...harness.host.querySelectorAll('[role="header"]'),
      ].find((item) => item.textContent.trim() === headingText);
      assert.ok(heading, `card heading not found: ${headingText}`);
      const card = heading.parentElement;
      return card?.querySelector('[role="alert"]');
    };
    const passwordError = () => cardAlert("Пароль");
    const identityError = () => cardAlert("Способы входа");
    let inlinePasswordError = passwordError();
    assert.ok(inlinePasswordError);
    const passwordCard = () => {
      let element = inlinePasswordError;
      while (element && !element.textContent.includes("Текущий пароль"))
        element = element.parentElement;
      return element;
    };
    assert.match(passwordCard()?.textContent ?? "", /Текущий пароль/);
    assert.match(passwordCard()?.textContent ?? "", /Изменить пароль/);
    assert.doesNotMatch(passwordCard()?.textContent ?? "", /Удаление аккаунта/);
    await harness.fill("Пароль для способов входа", "identity-password");
    await harness.click("Подключить Яндекс");
    await harness.flush();
    assert.match(
      identityError()?.textContent ?? "",
      /Сервис временно недоступен\./,
      "the OAuth action failure is shown in the identity card",
    );
    assert.match(
      passwordError()?.textContent ?? "",
      /Неверный email или пароль\./,
      "the password failure remains visible in the password card",
    );

    await harness.click("Изменить пароль");
    await harness.flush();
    assert.equal(typeof resolvePasswordRetry, "function");
    assert.equal(
      passwordError(),
      null,
      "retry clears stale password error immediately",
    );
    assert.ok(identityError(), "retry preserves the unrelated identity error");
    await act(async () =>
      resolvePasswordRetry(
        jsonResponse({ error: "temporarily_unavailable" }, 503),
      ),
    );
    await harness.flush();
    inlinePasswordError = passwordError();
    assert.ok(
      inlinePasswordError?.textContent.includes("Сервис временно недоступен."),
      "the new failure returns to the password card",
    );
    assert.ok(
      identityError(),
      "the identity error remains visible after the password retry fails",
    );
  } finally {
    await harness.unmount();
  }
});

test("notification settings preserve a dirty webhook draft across unrelated updates without pull-to-refresh", async () => {
  const harness = await createHarness();
  try {
    harness.native.restore = async () => ({
      account: { id: "account-1" },
      session: {
        id: "session-1",
        token: "token-1",
        expiresAt: "2099-01-01T00:00:00.000Z",
      },
    });
    await harness.apiModule.restoreSession();
    harness.setFetch(async (url, options) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors")
        return jsonResponse(mobileMonitorPage([mobileMonitor()]));
      if (path === "/monitors/monitor-1")
        return jsonResponse({ monitor: mobileMonitor() });
      if (path === "/monitors/monitor-1/results")
        return jsonResponse({ results: [] });
      if (path === "/monitors/monitor-1/incidents")
        return jsonResponse({ incidents: [] });
      if (path === "/messenger-destinations")
        return jsonResponse({ destinations: [] });
      if (path === "/monitors/monitor-1/messenger-routes")
        return jsonResponse({ routes: [] });
      if (path === "/monitors/monitor-1/webhook")
        return jsonResponse({
          webhook: {
            id: "webhook-1",
            url: "https://saved.example.test/hook",
            enabled: true,
            version: 1,
          },
        });
      if (path === "/monitors/monitor-1/push")
        return jsonResponse({ enabled: false, version: 0 });
      if (path === "/messenger-destinations/email/verification")
        return jsonResponse({ challenge: null });
      if (path === "/messenger-links" && options.method === "POST")
        return jsonResponse({
          link: {
            id: "pending-link",
            status: "pending",
            expiresAt: "2099-01-01T00:00:00.000Z",
          },
          startUrl: null,
          command: "/start test-code",
        });
      throw new Error(`Unexpected notification request: ${path}`);
    });
    await harness.renderMonitorsScreen();
    await harness.click("Primary monitor. Доступен");
    await harness.flush();
    await harness.click("Уведомления");
    await harness.click("Дополнительно: webhook");
    const webhookURL = harness.host.querySelector(
      'input[aria-label="HTTPS URL"]',
    );
    assert.ok(webhookURL);
    assert.equal(webhookURL.value, "https://saved.example.test/hook");
    assert.equal(
      harness.host.querySelector('button[aria-label="Обновить страницу"]') ===
        null,
      true,
      "the screen does not expose a generic pull refresh that can replace dirty settings",
    );
    await harness.click("Подключить адрес или чат аккаунта");
    await harness.fill("HTTPS URL", "https://draft.example.test/hook");
    await harness.click("Telegram · личный чат");
    await harness.flush();
    assert.match(harness.host.textContent, /\/start test-code/);
    assert.equal(
      harness.host.querySelector('input[aria-label="HTTPS URL"]').value,
      "https://draft.example.test/hook",
      "an unrelated successful link action re-renders without discarding the webhook draft",
    );
    assert.equal(
      harness.host.querySelector('button[aria-label="Обновить страницу"]') ===
        null,
      true,
    );
  } finally {
    await harness.unmount();
  }
});

test("a monitor draft survives the billing return without posting until the user starts it", async () => {
  const harness = await createHarness();
  const actor = {
    account: { id: "account-1" },
    session: {
      id: "session-1",
      token: "token-1",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  };
  let storedDraft = null;
  const monitorCreates = [];
  try {
    harness.native.restore = async () => actor;
    harness.native.restoreMonitorDraft = async (accountId, sessionId) =>
      accountId === actor.account.id && sessionId === actor.session.id
        ? storedDraft
        : null;
    harness.native.saveMonitorDraft = async (draft, accountId, sessionId) => {
      assert.equal(accountId, actor.account.id);
      assert.equal(sessionId, actor.session.id);
      storedDraft = structuredClone(draft);
    };
    harness.native.clearMonitorDraft = async (accountId, sessionId) => {
      assert.equal(accountId, actor.account.id);
      assert.equal(sessionId, actor.session.id);
      storedDraft = null;
    };
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: false,
      shopId: "",
      environment: "test",
    });
    harness.native.savePaymentIntent = async () => {};
    harness.native.clearPaymentIntent = async () => {};
    await harness.apiModule.restoreSession();
    harness.setMonitorEntitlement({
      quota: 1,
      enabledMonitorCount: 1,
      activeMonitorCount: 1,
      admin: null,
      packages: [],
    });
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors" && options.method === "POST") {
        const body = JSON.parse(options.body);
        monitorCreates.push(body);
        return jsonResponse({
          monitor: mobileMonitor({
            id: "monitor-created",
            name: body.name,
            url: body.url,
          }),
        });
      }
      if (path === "/monitors") return jsonResponse(mobileMonitorPage([]));
      if (path === "/monitors/monitor-created")
        return jsonResponse({
          monitor: mobileMonitor({
            id: "monitor-created",
            name: "Checkout return draft",
            url: "https://draft.example.test/health",
          }),
        });
      if (path === "/monitors/monitor-created/results")
        return jsonResponse({ results: [] });
      if (path === "/monitors/monitor-created/incidents")
        return jsonResponse({ incidents: [] });
      if (path === "/billing") return jsonResponse(billingOverview([]));
      if (path === "/billing/orders" && parsed.search === "?page=1&pageSize=10")
        return jsonResponse(billingPage([]));
      if (path === "/messenger-destinations")
        return jsonResponse({ destinations: [] });
      if (path === "/messenger-destinations/email/verification")
        return jsonResponse({ challenge: null });
      throw new Error(`Unexpected draft flow request: ${path}`);
    });
    await harness.renderDraftCheckoutFlow();
    await harness.click("Добавить монитор");
    await harness.fill("Название", "Checkout return draft");
    await harness.fill("URL HTTPS/HTTP", "https://draft.example.test/health");
    await harness.flush();
    assert.equal(storedDraft?.form.name, "Checkout return draft");
    assert.equal(
      monitorCreates.length,
      0,
      "editing alone does not create a monitor",
    );

    await harness.click("Купить квоту");
    await harness.flush();
    assert.match(harness.host.textContent, /Черновик монитора сохранён/);
    assert.equal(
      monitorCreates.length,
      0,
      "opening billing does not create a monitor",
    );

    harness.setMonitorEntitlement({
      quota: 2,
      enabledMonitorCount: 1,
      activeMonitorCount: 1,
      admin: null,
      packages: [],
    });
    await harness.click("Вернуться к настройке монитора");
    await harness.flush();
    assert.equal(
      harness.host.querySelector('input[aria-label="Название"]')?.value,
      "Checkout return draft",
      "the form name survives the billing screen unmount and return",
    );
    assert.equal(
      harness.host.querySelector('input[aria-label="URL HTTPS/HTTP"]')?.value,
      "https://draft.example.test/health",
      "the target URL survives the billing screen unmount and return",
    );
    assert.equal(
      monitorCreates.length,
      0,
      "returning never auto-posts the draft",
    );

    await harness.click("Запустить монитор");
    await harness.flush();
    assert.equal(
      monitorCreates.length,
      1,
      "only the explicit start action posts",
    );
    assert.equal(monitorCreates[0].name, "Checkout return draft");
    assert.equal(monitorCreates[0].url, "https://draft.example.test/health");
  } finally {
    await harness.unmount();
  }
});

test("pending quota-save cannot navigate to billing after logout unmounts the monitor editor", async () => {
  const harness = await createHarness();
  const actor = {
    account: { id: "account-1" },
    session: {
      id: "session-1",
      token: "token-1",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  };
  let resolveNativeSave;
  let saveStartedResolve;
  const saveStarted = new Promise((resolve) => {
    saveStartedResolve = resolve;
  });
  let billingNavigationCalls = 0;
  try {
    harness.native.restore = async () => actor;
    harness.native.restoreMonitorDraft = async () => null;
    harness.native.saveMonitorDraft = async () => {};
    harness.native.clearMonitorDraft = async () => {};
    await harness.apiModule.restoreSession();
    harness.setMonitorEntitlement({
      quota: 0,
      enabledMonitorCount: 0,
      activeMonitorCount: 0,
      admin: null,
      packages: [],
    });
    harness.setFetch(async (url) => {
      const path = new URL(url).pathname.replace("/api/v1", "");
      if (path === "/monitor-time-zones")
        return jsonResponse({ timeZones: [] });
      if (path === "/monitors") return jsonResponse(mobileMonitorPage([]));
      throw new Error(`Unexpected logout draft request: ${path}`);
    });
    await harness.renderMonitorsScreen(null, {
      onBuyQuota: () => billingNavigationCalls++,
    });
    await harness.click("Добавить монитор");
    await harness.fill("Название", "Pending checkout draft");
    await harness.fill("URL HTTPS/HTTP", "https://draft.example.test/health");
    await harness.flush();

    harness.native.saveMonitorDraft = async () => {
      saveStartedResolve();
      return new Promise((resolve) => {
        resolveNativeSave = resolve;
      });
    };
    await harness.click("Купить квоту");
    await saveStarted;
    assert.equal(
      billingNavigationCalls,
      0,
      "billing waits for the native draft write",
    );

    await harness.apiModule.closeSession();
    await harness.renderPage();
    assert.match(harness.host.textContent, /Содержимое/);
    await act(async () => resolveNativeSave());
    await harness.flush();
    assert.equal(
      billingNavigationCalls,
      0,
      "a completed save from the logged-out editor cannot trigger its stale navigation callback",
    );
    assert.doesNotMatch(harness.host.textContent, /Черновик монитора сохранён/);
  } finally {
    await harness.unmount();
  }
});

test("a late native draft restore from a previous account cannot replace the current account draft", async () => {
  const harness = await createHarness();
  const actorA = {
    account: { id: "account-a" },
    session: {
      id: "session-a",
      token: "token-a",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  };
  const actorB = {
    account: { id: "account-b" },
    session: {
      id: "session-b",
      token: "token-b",
      expiresAt: "2099-01-01T00:00:00.000Z",
    },
  };
  const deferred = () => {
    let resolve;
    const promise = new Promise((done) => {
      resolve = done;
    });
    return { promise, resolve };
  };
  const restoreA = deferred();
  const restoreB = deferred();
  const restoredFor = [];
  const draft = (name) => ({
    schemaVersion: 1,
    form: {
      checkType: "http",
      name,
      url: "https://draft.example.test/health",
      host: "",
      port: "443",
      method: "GET",
      expectedStatus: "200",
      bodyContains: "",
      intervalMinutes: "5",
      failureThreshold: "2",
      timeZone: "Europe/Moscow",
      enabled: true,
    },
    editId: null,
    savedAt: Date.now(),
  });
  try {
    harness.native.restore = async () => actorA;
    harness.native.restoreMonitorDraft = async (accountId, sessionId) => {
      restoredFor.push([accountId, sessionId]);
      if (accountId === actorA.account.id) return restoreA.promise;
      if (accountId === actorB.account.id) return restoreB.promise;
      return null;
    };
    await harness.apiModule.restoreSession();
    function DraftSnapshot() {
      const retained = harness.monitorDraftModule.useMonitorDraft();
      return React.createElement(
        "span",
        { "data-ready": String(retained.ready) },
        retained.draft?.form.name ?? "no draft",
      );
    }
    await harness.renderDraftProvider(
      React.createElement(DraftSnapshot),
      harness.apiModule.session(),
      `${actorA.account.id}:${actorA.session.id}`,
    );
    assert.deepEqual(restoredFor[0], [actorA.account.id, actorA.session.id]);

    harness.native.restore = async () => actorB;
    await harness.apiModule.restoreSession();
    await harness.renderDraftProvider(
      React.createElement(DraftSnapshot),
      harness.apiModule.session(),
      `${actorB.account.id}:${actorB.session.id}`,
    );
    assert.deepEqual(restoredFor[1], [actorB.account.id, actorB.session.id]);

    await act(async () => restoreB.resolve(draft("Current account draft")));
    await harness.flush();
    assert.match(harness.host.textContent, /Current account draft/);
    await act(async () => restoreA.resolve(draft("Previous account draft")));
    await harness.flush();
    assert.match(harness.host.textContent, /Current account draft/);
    assert.doesNotMatch(
      harness.host.textContent,
      /Previous account draft/,
      "a stale native restore from account A cannot overwrite account B's draft",
    );
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA navigation is confined to the current challenge and provider resources", async () => {
  const harness = await createHarness();
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const props = globalThis.__authWebViewProps;
    assert.ok(props);
    assert.equal(
      props.source.uri,
      `${challengeOrigin}/mobile/challenge?action=login&requestId=nonce_1`,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({ url: props.source.uri }),
      true,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: `${challengeOrigin}/mobile/challenge`,
      }),
      true,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "https://challenges.cloudflare.com/turnstile/v0/api.js",
      }),
      true,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "https://challenges.cloudflare.com.evil.test/x",
      }),
      false,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "https://challenges.cloudflare.com/x",
        isTopFrame: true,
      }),
      false,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: `${challengeOrigin}/mobile/challenge#fragment`,
      }),
      false,
    );
    assert.equal(
      props.onShouldStartLoadWithRequest({
        url: "http://challenges.cloudflare.com/x",
      }),
      false,
    );
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA accepts one fresh, matching, main-frame token and submits it once", async () => {
  const authed = [];
  const harness = await createHarness({
    tokens: ["fresh_request_nonce_1", "fresh_request_nonce_2"],
    onAuthed: async (value) => authed.push(value),
  });
  try {
    harness.setFetch(async (url, options) => {
      harness.requests.push({ url, options });
      return {
        ok: true,
        status: 200,
        headers: { get: () => "application/json" },
        json: async () => ({ account: {}, session: {} }),
      };
    });
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const props = globalThis.__authWebViewProps;
    const valid = {
      type: "turnstile",
      token: "cloudflare-token-long-enough",
      action: "login",
      requestId: "fresh_request_nonce_1",
    };
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify(valid),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify(valid),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.equal(harness.requests.length, 1);
    assert.equal(
      harness.requests[0].url,
      `${challengeOrigin}/api/v1/auth/native/login`,
    );
    assert.deepEqual(JSON.parse(harness.requests[0].options.body), {
      email: "person@example.com",
      password: "correct horse battery",
      turnstileToken: "cloudflare-token-long-enough",
    });
    assert.equal(
      harness.host.querySelector('[data-testid="auth-webview"]'),
      null,
    );
    assert.equal(authed.length, 1);
  } finally {
    await harness.unmount();
  }
});

test("callbacks from a retried CAPTCHA generation cannot submit or invalidate the current attempt", async () => {
  const harness = await createHarness({
    tokens: ["old_request_nonce", "new_request_nonce"],
  });
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const oldAttempt = globalThis.__authWebViewProps;
    await act(async () =>
      oldAttempt.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile_error",
            code: "timeout",
            action: "login",
            requestId: "old_request_nonce",
          }),
          url: challengeOrigin,
          isMainFrame: true,
        },
      }),
    );
    await harness.click("Повторить проверку");
    await harness.flush();
    const newAttempt = globalThis.__authWebViewProps;
    assert.equal(
      newAttempt.source.uri,
      `${challengeOrigin}/mobile/challenge?action=login&requestId=new_request_nonce`,
    );

    await act(async () =>
      oldAttempt.onNavigationStateChange({
        url: "https://evil.test/late-navigation",
      }),
    );
    await act(async () =>
      oldAttempt.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "old-token-long-enough",
            action: "login",
            requestId: "old_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.equal(
      globalThis.__authWebViewProps.source.uri,
      newAttempt.source.uri,
    );
    assert.equal(harness.requests.length, 0);

    await act(async () =>
      globalThis.__authWebViewProps.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "new-token-long-enough",
            action: "login",
            requestId: "new_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.equal(harness.requests.length, 1);
    assert.equal(
      JSON.parse(harness.requests[0].options.body).turnstileToken,
      "new-token-long-enough",
    );
  } finally {
    await harness.unmount();
  }
});

test("Android back press fences a CAPTCHA callback synchronously before cancellation", async () => {
  const harness = await createHarness({ tokens: ["back_request_nonce"] });
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    const cancelledAttempt = globalThis.__authWebViewProps;
    await act(async () => {
      harness.pressBack();
      cancelledAttempt.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "late-token-long-enough",
            action: "login",
            requestId: "back_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      });
    });
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Вход"));
    assert.equal(harness.requests.length, 0);
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA rejects mismatched source/action/request/frame and offers a fresh retry", async () => {
  const harness = await createHarness({
    tokens: [
      "request_one_nonce",
      "request_two_nonce",
      "request_three_nonce",
      "request_four_nonce",
    ],
  });
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    let props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "register",
            requestId: "request_one_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    assert.equal(
      props.source.uri,
      `${challengeOrigin}/mobile/challenge?action=login&requestId=request_two_nonce`,
    );
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "login",
            requestId: "stale_request_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "login",
            requestId: "request_three_nonce",
          }),
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: false,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile",
            token: "cloudflare-token-long-enough",
            action: "login",
            requestId: "request_four_nonce",
          }),
          url: "https://evil.test/",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не прошла"));
    assert.equal(harness.requests.length, 0);
  } finally {
    await harness.unmount();
  }
});

test("CAPTCHA handles malformed/provider errors, navigation escapes, and cancellation", async () => {
  const harness = await createHarness();
  try {
    await harness.fill("Email", "person@example.com");
    await harness.fill("Пароль", "correct horse battery");
    await harness.click("Войти");
    await harness.flush();
    let props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: "{",
          url: challengeOrigin + "/mobile/challenge",
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(
      harness.host.textContent.includes("Некорректный ответ проверки."),
    );
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onMessage({
        nativeEvent: {
          data: JSON.stringify({
            type: "turnstile_error",
            code: "timeout",
            action: "login",
            requestId: "nonce_2",
          }),
          url: challengeOrigin,
          isMainFrame: true,
        },
      }),
    );
    await harness.flush();
    assert.ok(harness.host.textContent.includes("Проверка не завершилась"));
    await harness.click("Повторить проверку");
    await harness.flush();
    props = globalThis.__authWebViewProps;
    await act(async () =>
      props.onNavigationStateChange({ url: "https://evil.test/redirect" }),
    );
    await harness.flush();
    assert.ok(
      harness.host.textContent.includes("Проверка открыла другую страницу"),
    );
    await harness.click("Отмена");
    assert.ok(harness.host.textContent.includes("Вход"));
    assert.equal(harness.requests.length, 0);
  } finally {
    await harness.unmount();
  }
});

test("challenge page renders compact markup without browser fallback controls", async () => {
  const challenge = await createWebChallenge();
  try {
    assert.equal(challenge.response.status, 200);
    const { document } = challenge.dom.window;
    assert.ok(document.querySelector("main.challenge"));
    assert.ok(document.querySelector("#widget"));
    assert.equal(document.querySelector("h1"), null);
    assert.equal(document.querySelector("#open"), null);
    assert.equal(document.querySelector("#fallback"), null);
    assert.equal(document.querySelectorAll("button").length, 1);
    assert.equal(document.querySelector("#retry").hidden, true);
    assert.equal(
      challenge.dom.window.getComputedStyle(document.querySelector("#retry"))
        .display,
      "none",
    );
  } finally {
    challenge.dom.window.close();
  }
});

test("challenge bridge binds action and nonce, scrubs query, restores history, and consumes a token once", async () => {
  const requestId = "A".repeat(43);
  const optionsList = [];
  const removed = [];
  const turnstile = {
    render(_element, options) {
      optionsList.push(options);
      return optionsList.length;
    },
    remove(id) {
      removed.push(id);
    },
  };
  const first = await createWebChallenge({
    turnstile,
    url: `https://heartbeat-checker.ru/mobile/challenge?action=register&requestId=${requestId}`,
  });
  try {
    assert.equal(first.dom.window.location.pathname, "/mobile/challenge");
    assert.equal(first.dom.window.location.search, "");
    assert.deepEqual(
      JSON.parse(JSON.stringify(first.dom.window.history.state)),
      {
        androidChallenge: {
          path: "/mobile/challenge",
          action: "register",
          requestId,
        },
      },
    );
    assert.equal(optionsList.length, 1);
    assert.equal(optionsList[0].action, "register");
    assert.equal(optionsList[0].sitekey, "test-site-key");
    const savedState = JSON.parse(
      JSON.stringify(first.dom.window.history.state),
    );
    first.dom.window.close();

    const second = await createWebChallenge({
      state: savedState,
      url: "https://heartbeat-checker.ru/mobile/challenge",
      turnstile,
    });
    try {
      assert.equal(optionsList.length, 2);
      assert.equal(optionsList[1].action, "register");
      optionsList[1].callback("turnstile-token-long-enough");
      optionsList[1].callback("duplicate-token-long-enough");
      assert.equal(second.messages.length, 1);
      assert.deepEqual(second.messages[0], {
        type: "turnstile",
        requestId,
        action: "register",
        token: "turnstile-token-long-enough",
      });
      assert.deepEqual(removed, [2]);
      assert.equal(
        second.dom.window.document.querySelector("#retry").hidden,
        true,
      );
    } finally {
      second.dom.window.close();
    }
  } finally {
    first.dom.window.close();
  }
});

test("challenge bridge rejects fragments and duplicate parameters without creating a widget", async (t) => {
  const requestId = "A".repeat(43);
  for (const [name, query] of [
    ["fragment", `?action=login&requestId=${requestId}#unexpected`],
    [
      "duplicate action",
      `?action=login&action=register&requestId=${requestId}`,
    ],
    [
      "duplicate request id",
      `?action=login&requestId=${requestId}&requestId=${requestId}`,
    ],
  ]) {
    await t.test(name, async () => {
      let renderCount = 0;
      const challenge = await createWebChallenge({
        url: `https://heartbeat-checker.ru/mobile/challenge${query}`,
        turnstile: {
          render() {
            renderCount++;
            return 1;
          },
        },
      });
      try {
        assert.equal(renderCount, 0);
        assert.ok(
          challenge.dom.window.document
            .querySelector("#status")
            .textContent.includes("недействителен"),
        );
        assert.equal(challenge.dom.window.history.state, null);
        assert.equal(challenge.messages.length, 0);
      } finally {
        challenge.dom.window.close();
      }
    });
  }
});

test("challenge provider timeout is bounded and retry can attach a late provider", async () => {
  let renderCount = 0;
  const provider = {
    render(_element, options) {
      renderCount++;
      this.options = options;
      return 7;
    },
    reset() {},
  };
  const challenge = await createWebChallenge();
  try {
    challenge.clock.advance(15_000);
    assert.ok(
      challenge.dom.window.document
        .querySelector("#status")
        .textContent.includes("Проверка недоступна"),
    );
    assert.equal(
      challenge.dom.window.document.querySelector("#retry").hidden,
      false,
    );
    assert.deepEqual(challenge.messages.at(-1), {
      type: "turnstile_error",
      requestId: "A".repeat(43),
      action: "login",
      code: "unavailable",
    });
    challenge.dom.window.turnstile = provider;
    challenge.dom.window.document.querySelector("#retry").click();
    assert.equal(renderCount, 1);
    assert.equal(
      challenge.dom.window.document.querySelector("#retry").hidden,
      true,
    );
    provider.options["timeout-callback"]();
    assert.equal(
      challenge.dom.window.document.querySelector("#retry").hidden,
      false,
    );
    challenge.dom.window.document.querySelector("#retry").click();
    provider.options.callback("turnstile-token-long-enough");
    assert.equal(challenge.messages.at(-1).type, "turnstile");
    assert.equal(
      challenge.messages.filter((message) => message.type === "turnstile")
        .length,
      1,
    );
  } finally {
    challenge.dom.window.close();
  }
});

function billingOrder(id, overrides = {}) {
  return {
    id,
    status: "canceled",
    purchaseKind: "add",
    monitorCount: 2,
    amountMinor: 4000,
    fulfillmentStatus: "none",
    createdAt: "2026-10-20T12:00:00Z",
    ...overrides,
  };
}

const futureBillingFixtureExpiry = new Date(
  Date.now() + 365 * 24 * 60 * 60 * 1000,
).toISOString();

const billingActor = {
  account: { id: "account-billing-test", email: "billing@example.test" },
  session: {
    id: "session-billing-test",
    token: "billing-test-token",
    expiresAt: futureBillingFixtureExpiry,
  },
};

function billingOverview(orders) {
  return {
    canPay: true,
    pricePerMonitorMinor: 2000,
    priceVersion: 1,
    orders,
  };
}

function billingEntitlement(quota = 0) {
  return {
    entitlement: {
      quota,
      enabledMonitorCount: 0,
      activeMonitorCount: 0,
      admin: null,
      packages: [],
    },
  };
}

function billingPage(orders, page = 1, total = orders.length, totalPages = 1) {
  return {
    orders,
    pagination: { page, pageSize: 10, total, totalPages },
  };
}

function sdkOrder(id, state, overrides = {}) {
  return billingOrder(id, {
    status: "pending",
    fulfillmentStatus: "pending",
    amountMinor: 2000,
    checkoutKind: "android_sdk",
    currency: "RUB",
    confirmationUrl: "https://secure.example.test/confirm",
    sdk: {
      state,
      shopId: "test-shop",
      environment: "test",
      paymentMethodType:
        state === "confirmation_required" ? "BANK_CARD" : undefined,
      draftExpiresAt: futureBillingFixtureExpiry,
      requestExpiresAt: futureBillingFixtureExpiry,
    },
    ...overrides,
  });
}

function sdkQuote() {
  return {
    shopId: "test-shop",
    environment: "test",
    amountMinor: 2000,
    currency: "RUB",
    title: "Мониторы",
    subtitle: "Покупка мониторов",
    savePaymentMethod: "off",
    allowedPaymentMethods: ["BANK_CARD"],
    draftExpiresAt: futureBillingFixtureExpiry,
  };
}

function sdkOverview(orders) {
  return {
    ...billingOverview(orders),
    nativeCheckout: {
      enabled: true,
      shopId: "test-shop",
      environment: "test",
      paymentMethods: ["BANK_CARD"],
    },
  };
}

async function prepareBillingHarness(harness, fetchBilling) {
  harness.native.restore = async () => billingActor;
  harness.native.restorePaymentIntent = async () => null;
  harness.native.sdkCapabilities = async () => ({
    configured: false,
    shopId: "",
    environment: "test",
  });
  harness.native.savePaymentIntent = async () => {};
  harness.native.clearPaymentIntent = async () => {};
  harness.native.DEV_CLIENT = true;
  harness.setFetch(async (url, options) => {
    const parsed = new URL(url);
    const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
    harness.requests.push({ url, options, path });
    return jsonResponse(await fetchBilling(path, options));
  });
  await harness.renderBillingScreen();
  await harness.flush();
}

test("billing history uses server pages and expanding history never inspects or replaces the active order", async () => {
  const harness = await createHarness();
  const activeId = "10000000-0000-4000-8000-000000000001";
  const oldId = "10000000-0000-4000-8000-000000000002";
  const latestActive = billingOrder(activeId, {
    status: "pending",
    fulfillmentStatus: "pending",
    createdAt: "2026-10-22T12:00:00Z",
  });
  const oldOrder = billingOrder(oldId);
  try {
    await prepareBillingHarness(harness, async (path) => {
      if (path === "/billing") return billingOverview([latestActive]);
      if (path === "/entitlement") return billingEntitlement();
      if (path === "/billing/orders?page=1&pageSize=10")
        return billingPage([latestActive, oldOrder], 1, 12, 2);
      if (path === `/billing/orders/${activeId}`)
        return { order: latestActive };
      if (path === "/billing/orders?page=2&pageSize=10")
        return billingPage(
          [billingOrder("10000000-0000-4000-8000-000000000003")],
          2,
          12,
          2,
        );
      throw new Error(`unexpected billing request ${path}`);
    });

    assert.match(harness.host.textContent, /Текущий заказ/);
    assert.doesNotMatch(harness.host.textContent, new RegExp(activeId));
    assert.match(harness.host.textContent, /История заказов/);
    const historyToggle = [...harness.host.querySelectorAll("button")].find(
      (button) => button.getAttribute("aria-label")?.includes("Подробнее"),
    );
    assert.ok(
      historyToggle,
      "history row should be an accessible disclosure control",
    );
    await act(async () => historyToggle.click());
    assert.match(harness.host.textContent, new RegExp(oldId));
    assert.equal(
      harness.requests.filter(
        (request) => request.path === `/billing/orders/${oldId}`,
      ).length,
      0,
      "opening a history row must stay a local detail expansion",
    );
    assert.doesNotMatch(harness.host.textContent, new RegExp(activeId));

    await harness.click("Далее");
    assert.ok(
      harness.requests.some(
        (request) => request.path === "/billing/orders?page=2&pageSize=10",
      ),
      "pagination must request the server's second page",
    );
    assert.match(harness.host.textContent, /2 из 2/);
    assert.doesNotMatch(harness.host.textContent, new RegExp(activeId));
    assert.equal(
      harness.requests.some((request) => request.options?.method === "POST"),
      false,
      "reading history must not create a payment order",
    );
  } finally {
    await harness.unmount();
  }
});

test("a late SDK token response cannot roll a terminal paid order back to processing", async () => {
  const harness = await createHarness();
  const clock = fakeTimerRuntime();
  const activeId = "15000000-0000-4000-8000-000000000001";
  const awaitingToken = sdkOrder(activeId, "awaiting_token");
  const processing = sdkOrder(activeId, "processing");
  const paid = sdkOrder(activeId, "complete", {
    status: "succeeded",
    fulfillmentStatus: "fulfilled",
  });
  let storedIntent = null;
  let overviewReads = 0;
  let entitlementReads = 0;
  let historyReads = 0;
  let statusReads = 0;
  let resolveTokenResponse;
  let resolveLateStatus;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => storedIntent;
    harness.native.savePaymentIntent = async (value) => {
      storedIntent = value;
    };
    harness.native.clearPaymentIntent = async () => {
      storedIntent = null;
    };
    harness.native.randomUUID = async () => "token-race-key";
    harness.native.sdkCapabilities = async () => ({
      configured: true,
      shopId: "test-shop",
      environment: "test",
    });
    harness.native.sdkTokenize = async () => ({
      status: "tokenized",
      paymentToken: "one-time-token",
      paymentMethodType: "BANK_CARD",
    });
    harness.native.DEV_CLIENT = false;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      const method = options.method ?? "GET";
      harness.requests.push({ url, options, path });
      if (path === "/billing" && method === "GET") {
        overviewReads++;
        return jsonResponse(
          sdkOverview(
            overviewReads === 1
              ? []
              : overviewReads > 2
                ? [paid]
                : [awaitingToken],
          ),
        );
      }
      if (path === "/entitlement" && method === "GET") {
        entitlementReads++;
        return jsonResponse(billingEntitlement(entitlementReads > 2 ? 2 : 0));
      }
      if (path === "/billing/orders?page=1&pageSize=10" && method === "GET") {
        historyReads++;
        return jsonResponse(
          billingPage(
            historyReads > 2
              ? [paid]
              : historyReads === 1
                ? []
                : [awaitingToken],
          ),
        );
      }
      if (path === "/billing/native/orders" && method === "POST")
        return jsonResponse({ order: awaitingToken, sdk: sdkQuote() });
      if (
        path === `/billing/native/orders/${activeId}/token` &&
        method === "POST"
      )
        return await new Promise((resolve) => {
          resolveTokenResponse = () =>
            resolve(jsonResponse({ order: processing }));
        });
      if (path === `/billing/orders/${activeId}` && method === "GET") {
        statusReads++;
        if (statusReads === 1) return jsonResponse({ order: awaitingToken });
        if (statusReads === 2) return jsonResponse({ order: processing });
        if (statusReads === 3) return jsonResponse({ order: paid });
        if (statusReads === 4)
          return await new Promise((resolve) => {
            resolveLateStatus = () =>
              resolve(jsonResponse({ order: processing }));
          });
        return jsonResponse({ order: paid });
      }
      throw new Error(`unexpected billing request ${method} ${path}`);
    });
    await harness.renderBillingScreen(null, clock);
    await harness.flush();
    await harness.click("Оплатить картой");
    await harness.flush();
    assert.equal(
      typeof resolveTokenResponse,
      "function",
      "token attachment should be pending",
    );
    assert.equal(typeof storedIntent?.orderId, "string");

    await act(async () => clock.advance(3_000));
    await harness.flush();
    assert.match(harness.host.textContent, /Доступная квота: 2/);
    assert.equal(
      storedIntent,
      null,
      "the paid order should clear its saved checkout intent",
    );
    assert.ok(overviewReads >= 3 && entitlementReads >= 3 && historyReads >= 3);

    resolveTokenResponse();
    await harness.flush();
    if (resolveLateStatus) {
      assert.match(
        harness.host.textContent,
        /Доступная квота: 2/,
        "a late processing POST must not flash over the paid order",
      );
      resolveLateStatus();
      await harness.flush();
    }
    assert.match(harness.host.textContent, /Доступная квота: 2/);
    assert.match(harness.host.textContent, /Оплачен · квота начислена/);
    assert.equal(
      harness.requests.filter(
        (request) =>
          request.options.method === "POST" &&
          request.path.startsWith("/billing/native/orders"),
      ).length,
      2,
      "polling and stale response reconciliation must not create a second order or token",
    );
    assert.equal(
      harness.host.textContent.includes(`ID для поддержки: ${activeId}`),
      false,
      "the full order ID should remain hidden until the history row is expanded",
    );
  } finally {
    await harness.unmount();
  }
});

test("billing polling observes a paid order, refreshes quota and history, and never starts a payment", async () => {
  const harness = await createHarness();
  const activeId = "20000000-0000-4000-8000-000000000001";
  const pending = billingOrder(activeId, {
    status: "pending",
    fulfillmentStatus: "pending",
  });
  const paid = billingOrder(activeId, {
    status: "succeeded",
    fulfillmentStatus: "fulfilled",
    monitorCount: 2,
  });
  let statusReads = 0;
  let overviewReads = 0;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: false,
      shopId: "",
      environment: "test",
    });
    harness.native.savePaymentIntent = async () => {};
    harness.native.clearPaymentIntent = async () => {};
    harness.native.DEV_CLIENT = true;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      harness.requests.push({ url, options, path });
      if (path === "/billing") {
        overviewReads++;
        return jsonResponse(
          billingOverview([overviewReads > 1 ? paid : pending]),
        );
      }
      if (path === "/entitlement")
        return jsonResponse(billingEntitlement(overviewReads > 1 ? 2 : 0));
      if (path === "/billing/orders?page=1&pageSize=10")
        return jsonResponse(billingPage([overviewReads > 1 ? paid : pending]));
      if (path === `/billing/orders/${activeId}`) {
        statusReads++;
        return jsonResponse({ order: paid });
      }
      throw new Error(`unexpected billing request ${path}`);
    });
    await harness.renderBillingScreen();
    await harness.flush();

    assert.ok(
      statusReads >= 1,
      "an active pending order should be polled immediately",
    );
    assert.match(harness.host.textContent, /Доступная квота: 2/);
    assert.match(
      harness.host.textContent,
      /Оплата и начисление подтверждены сервером\./,
    );
    assert.ok(
      overviewReads >= 2,
      "paid status should refresh the billing overview",
    );
    assert.equal(
      harness.requests.some((request) => request.options?.method === "POST"),
      false,
      "status polling and reconciliation must remain read-only",
    );
  } finally {
    await harness.unmount();
  }
});

test("billing polling ignores a late paid response after backgrounding", async () => {
  const harness = await createHarness();
  const activeId = "30000000-0000-4000-8000-000000000001";
  const pending = billingOrder(activeId, {
    status: "pending",
    fulfillmentStatus: "pending",
  });
  const paid = billingOrder(activeId, {
    status: "succeeded",
    fulfillmentStatus: "fulfilled",
  });
  let resolveStatus;
  let statusSignal;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: false,
      shopId: "",
      environment: "test",
    });
    harness.native.DEV_CLIENT = true;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      harness.requests.push({ url, options, path });
      if (path === "/billing") return jsonResponse(billingOverview([pending]));
      if (path === "/entitlement") return jsonResponse(billingEntitlement());
      if (path === "/billing/orders?page=1&pageSize=10")
        return jsonResponse(billingPage([pending]));
      if (path === `/billing/orders/${activeId}`) {
        statusSignal = options.signal;
        return await new Promise((resolve) => {
          resolveStatus = () => resolve(jsonResponse({ order: paid }));
        });
      }
      throw new Error(`unexpected billing request ${path}`);
    });
    await harness.renderBillingScreen();
    await harness.flush();
    assert.equal(
      typeof resolveStatus,
      "function",
      "poll should start immediately",
    );

    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("background"),
    );
    assert.ok(statusSignal, "the shared status GET should have started");
    resolveStatus();
    await harness.flush();

    assert.match(harness.host.textContent, /Доступная квота: 0/);
    assert.equal(
      harness.requests.filter((request) => request.path === "/billing").length,
      1,
      "a stale paid response must not trigger entitlement reconciliation",
    );
    assert.equal(
      harness.requests.some((request) => request.options?.method === "POST"),
      false,
    );
  } finally {
    await harness.unmount();
  }
});

test("billing does not poll terminal, blocked, manual-review, or stopped orders", async () => {
  const cases = [
    billingOrder("40000000-0000-4000-8000-000000000001", {
      status: "canceled",
    }),
    billingOrder("40000000-0000-4000-8000-000000000002", {
      status: "succeeded",
      fulfillmentStatus: "blocked",
    }),
    billingOrder("40000000-0000-4000-8000-000000000003", {
      status: "pending",
      fulfillmentStatus: "pending",
      reconciliationStatus: "manual_review",
    }),
    billingOrder("40000000-0000-4000-8000-000000000004", {
      status: "pending",
      fulfillmentStatus: "pending",
      reconciliationStatus: "stopped",
    }),
  ];
  for (const item of cases) {
    const harness = await createHarness();
    try {
      await prepareBillingHarness(harness, async (path) => {
        if (path === "/billing") return billingOverview([item]);
        if (path === "/entitlement") return billingEntitlement();
        if (path === "/billing/orders?page=1&pageSize=10")
          return billingPage([item]);
        if (path === `/billing/orders/${item.id}`)
          throw new Error(
            `terminal or reconciliation-stopped order was polled: ${item.id}`,
          );
        throw new Error(`unexpected billing request ${path}`);
      });
      assert.equal(
        harness.requests.some(
          (request) => request.path === `/billing/orders/${item.id}`,
        ),
        false,
        `order ${item.id} must not be polled`,
      );
    } finally {
      await harness.unmount();
    }
  }
});

test("billing polling times out one GET, retries sequentially, and leaves the order readable", async () => {
  const harness = await createHarness();
  const clock = fakeTimerRuntime();
  const activeId = "50000000-0000-4000-8000-000000000001";
  const pending = billingOrder(activeId, {
    status: "pending",
    fulfillmentStatus: "pending",
  });
  let activeReads = 0;
  let outstanding = 0;
  let maxOutstanding = 0;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: false,
      shopId: "",
      environment: "test",
    });
    harness.native.DEV_CLIENT = true;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      harness.requests.push({ url, options, path });
      if (path === "/billing") return jsonResponse(billingOverview([pending]));
      if (path === "/entitlement") return jsonResponse(billingEntitlement());
      if (path === "/billing/orders?page=1&pageSize=10")
        return jsonResponse(billingPage([pending]));
      if (path === `/billing/orders/${activeId}`) {
        activeReads++;
        outstanding++;
        maxOutstanding = Math.max(maxOutstanding, outstanding);
        return await new Promise((resolve, reject) => {
          const signal = options.signal;
          signal.addEventListener(
            "abort",
            () => {
              outstanding--;
              const error = new Error("aborted by request timeout");
              error.name = "AbortError";
              reject(error);
            },
            { once: true },
          );
        });
      }
      throw new Error(`unexpected billing request ${path}`);
    });
    await harness.renderBillingScreen(null, clock);
    await harness.flush();
    assert.equal(activeReads, 1, "one GET should begin immediately");
    assert.equal(outstanding, 1);

    await act(async () => clock.advance(10_000));
    await harness.flush();
    assert.equal(
      outstanding,
      0,
      "the 10-second request timer must abort the GET",
    );
    assert.equal(activeReads, 1, "retry should wait for the 3-second interval");

    await act(async () => clock.advance(3_000));
    await harness.flush();
    assert.equal(
      activeReads,
      2,
      "a failed read may retry while the order is active",
    );
    assert.equal(maxOutstanding, 1, "status reads must never overlap");
    assert.match(harness.host.textContent, /Текущий заказ/);
  } finally {
    await harness.unmount();
  }
});

test("billing polling stops after the 90-second visible window", async () => {
  const harness = await createHarness();
  const clock = fakeTimerRuntime();
  const activeId = "55000000-0000-4000-8000-000000000001";
  const pending = billingOrder(activeId, {
    status: "pending",
    fulfillmentStatus: "pending",
  });
  let statusReads = 0;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: false,
      shopId: "",
      environment: "test",
    });
    harness.native.DEV_CLIENT = true;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      harness.requests.push({ url, options, path });
      if (path === "/billing") return jsonResponse(billingOverview([pending]));
      if (path === "/entitlement") return jsonResponse(billingEntitlement());
      if (path === "/billing/orders?page=1&pageSize=10")
        return jsonResponse(billingPage([pending]));
      if (path === `/billing/orders/${activeId}`) {
        statusReads++;
        return jsonResponse({ order: pending });
      }
      throw new Error(`unexpected billing request ${path}`);
    });
    await harness.renderBillingScreen(null, clock);
    await harness.flush();

    for (let index = 0; index < 30; index++) {
      await act(async () => clock.advance(3_000));
      await harness.flush();
    }
    const stoppedAt = statusReads;
    assert.ok(
      stoppedAt <= 30,
      "polling should remain within its 90-second budget",
    );
    assert.match(harness.host.textContent, /Заказ ещё обрабатывается/);
    await act(async () => clock.advance(30_000));
    await harness.flush();
    assert.equal(
      statusReads,
      stoppedAt,
      "no GET should run after the visible budget expires",
    );
  } finally {
    await harness.unmount();
  }
});

test("billing discards a paid status response that arrives after logout", async () => {
  const harness = await createHarness();
  const activeId = "60000000-0000-4000-8000-000000000001";
  const pending = billingOrder(activeId, {
    status: "pending",
    fulfillmentStatus: "pending",
  });
  const paid = billingOrder(activeId, {
    status: "succeeded",
    fulfillmentStatus: "fulfilled",
  });
  let resolveStatus;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: false,
      shopId: "",
      environment: "test",
    });
    harness.native.DEV_CLIENT = true;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      harness.requests.push({ url, options, path });
      if (path === "/billing") return jsonResponse(billingOverview([pending]));
      if (path === "/entitlement") return jsonResponse(billingEntitlement());
      if (path === "/billing/orders?page=1&pageSize=10")
        return jsonResponse(billingPage([pending]));
      if (path === `/billing/orders/${activeId}`)
        return await new Promise((resolve) => {
          resolveStatus = () => resolve(jsonResponse({ order: paid }));
        });
      throw new Error(`unexpected billing request ${path}`);
    });
    await harness.renderBillingScreen();
    await harness.flush();
    assert.equal(
      typeof resolveStatus,
      "function",
      "poll should start immediately",
    );

    await harness.apiModule.closeSession();
    resolveStatus();
    await harness.flush();

    assert.match(harness.host.textContent, /Доступная квота: 0/);
    assert.equal(
      harness.requests.filter((request) => request.path === "/billing").length,
      1,
      "a response from before logout must not refresh the former account",
    );
    assert.equal(harness.native.logoutCalls, 1);
  } finally {
    await harness.unmount();
  }
});

test("billing rejects a mismatched order response without replacing the active order", async () => {
  const harness = await createHarness();
  const activeId = "70000000-0000-4000-8000-000000000001";
  const otherId = "70000000-0000-4000-8000-000000000002";
  const pending = billingOrder(activeId, {
    status: "pending",
    fulfillmentStatus: "pending",
  });
  try {
    await prepareBillingHarness(harness, async (path) => {
      if (path === "/billing") return billingOverview([pending]);
      if (path === "/entitlement") return billingEntitlement();
      if (path === "/billing/orders?page=1&pageSize=10")
        return billingPage([pending]);
      if (path === `/billing/orders/${activeId}`)
        return { order: billingOrder(otherId, { status: "succeeded" }) };
      throw new Error(`unexpected billing request ${path}`);
    });
    assert.match(harness.host.textContent, /Текущий заказ/);
    assert.doesNotMatch(harness.host.textContent, new RegExp(otherId));
    assert.equal(
      harness.requests.some(
        (request) => request.path === `/billing/orders/${otherId}`,
      ),
      false,
      "a mismatched response must not cause another order to be inspected",
    );
  } finally {
    await harness.unmount();
  }
});

test("billing confirmation joins a poll's in-flight status read instead of failing as superseded", async () => {
  const harness = await createHarness();
  const clock = fakeTimerRuntime();
  const activeId = "80000000-0000-4000-8000-000000000001";
  const confirmation = sdkOrder(activeId, "confirmation_required");
  let statusReads = 0;
  let resolvePreflight;
  let preflightSignal;
  let confirmations = 0;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: true,
      shopId: "test-shop",
      environment: "test",
    });
    harness.native.sdkConfirm = async () => {
      confirmations++;
      return { status: "completed" };
    };
    harness.native.DEV_CLIENT = false;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      harness.requests.push({ url, options, path });
      if (path === "/billing") return jsonResponse(sdkOverview([confirmation]));
      if (path === "/entitlement") return jsonResponse(billingEntitlement());
      if (path === "/billing/orders?page=1&pageSize=10")
        return jsonResponse(billingPage([confirmation]));
      if (path === `/billing/orders/${activeId}`) {
        statusReads++;
        if (statusReads === 1) return jsonResponse({ order: confirmation });
        if (statusReads === 2) {
          preflightSignal = options.signal;
          return await new Promise((resolve) => {
            resolvePreflight = () =>
              resolve(jsonResponse({ order: confirmation }));
          });
        }
        return jsonResponse({ order: confirmation });
      }
      throw new Error(`unexpected billing request ${path}`);
    });
    await harness.renderBillingScreen(null, clock);
    await harness.flush();
    assert.equal(
      statusReads,
      1,
      "the active order should start one immediate poll",
    );

    await harness.click("Подтвердить в банке");
    await harness.flush();
    assert.equal(
      typeof resolvePreflight,
      "function",
      "confirmation should preflight the order",
    );
    await act(async () => clock.advance(3_000));
    await harness.flush();

    assert.equal(
      statusReads,
      2,
      "the poll tick should join the confirmation's pending GET",
    );
    await act(async () =>
      harness.nativeMocks.AppState.setCurrentState("background"),
    );
    assert.equal(
      preflightSignal?.aborted,
      false,
      "backgrounding must not abort the shared GET still needed by confirmation",
    );
    resolvePreflight();
    await harness.flush();
    assert.equal(confirmations, 1, "bank confirmation should run exactly once");
    assert.doesNotMatch(
      harness.host.textContent,
      /Не удалось подключиться|Order request superseded|Запрос заказа/u,
      "a shared read must not surface as an application error",
    );
  } finally {
    await harness.unmount();
  }
});

test("a terminal bank-confirmation response refreshes quota and history, with UUID details revealed on demand", async () => {
  const harness = await createHarness();
  const activeId = "90000000-0000-4000-8000-000000000001";
  const confirmation = sdkOrder(activeId, "confirmation_required");
  const paid = sdkOrder(activeId, "complete", {
    status: "succeeded",
    fulfillmentStatus: "fulfilled",
  });
  let statusReads = 0;
  let overviewReads = 0;
  let entitlementReads = 0;
  let historyReads = 0;
  try {
    harness.native.restore = async () => billingActor;
    harness.native.restorePaymentIntent = async () => null;
    harness.native.sdkCapabilities = async () => ({
      configured: true,
      shopId: "test-shop",
      environment: "test",
    });
    harness.native.sdkConfirm = async () => ({ status: "completed" });
    harness.native.DEV_CLIENT = false;
    harness.setFetch(async (url, options) => {
      const parsed = new URL(url);
      const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
      harness.requests.push({ url, options, path });
      if (path === "/billing") {
        overviewReads++;
        return jsonResponse(
          sdkOverview([overviewReads > 1 ? paid : confirmation]),
        );
      }
      if (path === "/entitlement") {
        entitlementReads++;
        return jsonResponse(billingEntitlement(entitlementReads > 1 ? 2 : 0));
      }
      if (path === "/billing/orders?page=1&pageSize=10") {
        historyReads++;
        return jsonResponse(
          billingPage([historyReads > 1 ? paid : confirmation]),
        );
      }
      if (path === `/billing/orders/${activeId}`) {
        statusReads++;
        return jsonResponse({ order: statusReads >= 3 ? paid : confirmation });
      }
      throw new Error(`unexpected billing request ${path}`);
    });
    await harness.renderBillingScreen();
    await harness.flush();
    await harness.click("Подтвердить в банке");
    await harness.flush();

    assert.ok(
      overviewReads >= 2,
      "terminal status should refresh the billing overview",
    );
    assert.ok(
      entitlementReads >= 2,
      "terminal status should refresh the quota",
    );
    assert.ok(
      historyReads >= 2,
      "terminal status should refresh the visible history page",
    );
    assert.match(harness.host.textContent, /Доступная квота: 2/);
    assert.match(harness.host.textContent, /Оплачен · квота начислена/);
    assert.doesNotMatch(
      harness.host.textContent,
      new RegExp(`ID для поддержки: ${activeId}`),
      "a terminal history row should keep its full ID collapsed",
    );
    const historyToggle = [...harness.host.querySelectorAll("button")].find(
      (button) => button.getAttribute("aria-label")?.includes("Подробнее"),
    );
    assert.ok(
      historyToggle,
      "the terminal order should remain discoverable in history",
    );
    await act(async () => historyToggle.click());
    assert.match(
      harness.host.textContent,
      new RegExp(`ID для поддержки: ${activeId}`),
    );
  } finally {
    await harness.unmount();
  }
});

test("a manual status check can advance manual-review and stopped orders to paid", async () => {
  for (const [reconciliationStatus, suffix] of [
    ["manual_review", "1"],
    ["stopped", "2"],
  ]) {
    const harness = await createHarness();
    const id = `91000000-0000-4000-8000-00000000000${suffix}`;
    const held = billingOrder(id, {
      status: "pending",
      fulfillmentStatus: "pending",
      reconciliationStatus,
    });
    const paid = billingOrder(id, {
      status: "succeeded",
      fulfillmentStatus: "fulfilled",
    });
    let statusReads = 0;
    let overviewReads = 0;
    let entitlementReads = 0;
    let historyReads = 0;
    try {
      harness.native.restore = async () => billingActor;
      harness.native.restorePaymentIntent = async () => null;
      harness.native.sdkCapabilities = async () => ({
        configured: false,
        shopId: "",
        environment: "test",
      });
      harness.native.DEV_CLIENT = true;
      harness.setFetch(async (url, options) => {
        const parsed = new URL(url);
        const path = parsed.pathname.replace(/^\/api\/v1/, "") + parsed.search;
        harness.requests.push({ url, options, path });
        if (path === "/billing") {
          overviewReads++;
          return jsonResponse(
            billingOverview([overviewReads > 1 ? paid : held]),
          );
        }
        if (path === "/entitlement") {
          entitlementReads++;
          return jsonResponse(billingEntitlement(entitlementReads > 1 ? 2 : 0));
        }
        if (path === "/billing/orders?page=1&pageSize=10") {
          historyReads++;
          return jsonResponse(billingPage([historyReads > 1 ? paid : held]));
        }
        if (path === `/billing/orders/${id}`) {
          statusReads++;
          return jsonResponse({ order: paid });
        }
        throw new Error(`unexpected billing request ${path}`);
      });
      await harness.renderBillingScreen();
      await harness.flush();
      assert.equal(
        statusReads,
        0,
        `${reconciliationStatus} must not be auto-polled`,
      );
      await harness.click("Проверить статус");
      await harness.flush();

      assert.equal(statusReads, 1);
      assert.ok(
        overviewReads >= 2,
        "terminal status should refresh billing once",
      );
      assert.ok(entitlementReads >= 2, "terminal status should refresh quota");
      assert.ok(historyReads >= 2, "terminal status should refresh history");
      assert.match(harness.host.textContent, /Доступная квота: 2/);
      assert.match(harness.host.textContent, /Оплачен · квота начислена/);
    } finally {
      await harness.unmount();
    }
  }
});

test("a manual check that reopens a stopped order restarts bounded polling", async () => {
  const harness = await createHarness();
  const id = "92000000-0000-4000-8000-000000000001";
  const stopped = billingOrder(id, {
    status: "pending",
    fulfillmentStatus: "pending",
    reconciliationStatus: "stopped",
  });
  const reopened = billingOrder(id, {
    status: "pending",
    fulfillmentStatus: "pending",
  });
  let statusReads = 0;
  try {
    await prepareBillingHarness(harness, async (path) => {
      if (path === "/billing") return billingOverview([stopped]);
      if (path === "/entitlement") return billingEntitlement();
      if (path === "/billing/orders?page=1&pageSize=10")
        return billingPage([stopped]);
      if (path === `/billing/orders/${id}`) {
        statusReads++;
        return { order: reopened };
      }
      throw new Error(`unexpected billing request ${path}`);
    });
    assert.equal(
      statusReads,
      0,
      "a stopped order should wait for an explicit status check",
    );
    await harness.click("Проверить статус");
    await harness.flush();
    assert.ok(
      statusReads >= 2,
      "polling should resume after the server reopens the order",
    );
    assert.doesNotMatch(harness.host.textContent, /Нужна проверка/);
  } finally {
    await harness.unmount();
  }
});
