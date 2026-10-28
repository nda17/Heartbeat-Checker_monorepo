import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  AccessibilityInfo,
  ActivityIndicator,
  Animated,
  AppState,
  Image,
  KeyboardAvoidingView,
  Pressable,
  RefreshControl,
  StyleProp,
  StyleSheet,
  Text,
  TextInput,
  TextInputProps,
  useColorScheme,
  View,
  ViewStyle,
} from "react-native";

const iconSources = {
  activity: require("../assets/icon-activity.png"),
  payment: require("../assets/icon-payment.png"),
  settings: require("../assets/icon-settings.png"),
  shield: require("../assets/icon-shield.png"),
  logout: require("../assets/icon-logout.png"),
  pause: require("../assets/icon-pause.png"),
  bell: require("../assets/icon-bell.png"),
  trash: require("../assets/icon-trash.png"),
  plus: require("../assets/icon-plus.png"),
  refresh: require("../assets/icon-refresh.png"),
  close: require("../assets/icon-close.png"),
  info: require("../assets/icon-info.png"),
  check: require("../assets/icon-check.png"),
  alert: require("../assets/icon-alert.png"),
  mail: require("../assets/icon-mail.png"),
  "arrow-up-right": require("../assets/icon-arrow-up-right.png"),
  "arrow-right": require("../assets/icon-arrow-right.png"),
  "arrow-left": require("../assets/icon-arrow-left.png"),
  "arrow-down": require("../assets/icon-arrow-down.png"),
  globe: require("../assets/icon-globe.png"),
  user: require("../assets/icon-user.png"),
  yandex: require("../assets/yandex.png"),
  vk: require("../assets/vk.png"),
  max: require("../assets/max.png"),
  telegram: require("../assets/telegram.png"),
} as const;

export type IconName = keyof typeof iconSources;

const MotionEnabled = createContext(false);
const AnimatedPressable = Animated.createAnimatedComponent(Pressable);

export function MotionPreferences({ children }: { children: React.ReactNode }) {
  const [reduced, setReduced] = useState<boolean | null>(null);
  const [active, setActive] = useState(AppState.currentState === "active");
  useEffect(() => {
    let mounted = true;
    let changed = false;
    const motion = AccessibilityInfo.addEventListener(
      "reduceMotionChanged",
      (value) => {
        if (!mounted) return;
        changed = true;
        setReduced(value);
      },
    );
    void AccessibilityInfo.isReduceMotionEnabled()
      .then((value) => {
        if (mounted && !changed) setReduced(value);
      })
      .catch(() => {
        if (mounted && !changed) setReduced(true);
      });
    const app = AppState.addEventListener("change", (state) => {
      if (mounted) setActive(state === "active");
    });
    return () => {
      mounted = false;
      motion.remove();
      app.remove();
    };
  }, []);
  return (
    <MotionEnabled.Provider value={active && reduced === false}>
      {children}
    </MotionEnabled.Provider>
  );
}

export const useMotionEnabled = () => useContext(MotionEnabled);

export function GlitchAccent({
  children,
  active = true,
  style,
}: {
  children: React.ReactNode;
  active?: boolean;
  style?: StyleProp<ViewStyle>;
}) {
  const enabled = useMotionEnabled();
  const c = usePalette();
  const phase = useRef(new Animated.Value(0)).current;
  useEffect(() => {
    phase.stopAnimation();
    phase.setValue(0);
    if (!enabled || !active) return;
    const animation = Animated.loop(
      Animated.sequence([
        Animated.timing(phase, {
          toValue: 0,
          duration: 6200,
          useNativeDriver: true,
          isInteraction: false,
        }),
        Animated.timing(phase, {
          toValue: 1,
          duration: 65,
          useNativeDriver: true,
          isInteraction: false,
        }),
        Animated.timing(phase, {
          toValue: -1,
          duration: 65,
          useNativeDriver: true,
          isInteraction: false,
        }),
        Animated.timing(phase, {
          toValue: 0,
          duration: 90,
          useNativeDriver: true,
          isInteraction: false,
        }),
      ]),
    );
    animation.start();
    return () => {
      animation.stop();
      phase.stopAnimation();
      phase.setValue(0);
    };
  }, [active, enabled, phase]);
  return (
    <View style={style}>
      {children}
      <Animated.View
        pointerEvents="none"
        accessibilityElementsHidden
        importantForAccessibility="no-hide-descendants"
        style={[
          StyleSheet.absoluteFill,
          {
            overflow: "hidden",
            opacity: phase.interpolate({
              inputRange: [-1, 0, 1],
              outputRange: [0.38, 0, 0.3],
            }),
            transform: [
              {
                translateX: phase.interpolate({
                  inputRange: [-1, 1],
                  outputRange: [-2, 2],
                }),
              },
            ],
          },
        ]}
      >
        {children}
        <View
          style={[
            styles.glitchLine,
            { top: "31%", backgroundColor: c.accentText },
          ]}
        />
        <View
          style={[
            styles.glitchLine,
            { top: "65%", backgroundColor: c.dangerText },
          ]}
        />
      </Animated.View>
    </View>
  );
}

export function SignalAccent({ motionKey }: { motionKey: string }) {
  const c = usePalette();
  const progress = useEntryMotion(motionKey);
  return (
    <Animated.View
      pointerEvents="none"
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      style={[
        styles.signal,
        {
          borderColor: c.border,
          backgroundColor: c.bg,
          opacity: progress,
          transform: [
            {
              translateY: progress.interpolate({
                inputRange: [0, 1],
                outputRange: [5, 0],
              }),
            },
          ],
        },
      ]}
    >
      <View style={[styles.signalDot, { backgroundColor: c.accentText }]} />
      <View style={styles.signalWave}>
        {(
          [
            { left: 0, top: 18, width: 28, rotate: "0deg" },
            { left: 23, top: 12, width: 20, rotate: "-50deg" },
            { left: 35, top: 17, width: 34, rotate: "65deg" },
            { left: 55, top: 20, width: 29, rotate: "-65deg" },
            { left: 75, top: 15, width: 18, rotate: "40deg" },
            { left: 89, top: 18, width: 47, rotate: "0deg" },
          ] as const
        ).map((segment, index) => (
          <View
            key={index}
            style={[
              styles.signalLine,
              {
                left: segment.left,
                top: segment.top,
                width: segment.width,
                backgroundColor: c.accentText,
                transform: [{ rotate: segment.rotate }],
              },
            ]}
          />
        ))}
      </View>
      <View style={[styles.signalGrid, { borderColor: c.border }]} />
    </Animated.View>
  );
}

export function useEntryMotion(
  motionKey: string | number | undefined,
  animateOnMount = true,
) {
  const enabled = useMotionEnabled();
  const progress = useRef(new Animated.Value(1)).current;
  const previous = useRef(motionKey);
  const initial = useRef(true);
  useEffect(() => {
    const changed = previous.current !== motionKey;
    const enter = changed || (initial.current && animateOnMount);
    initial.current = false;
    previous.current = motionKey;
    progress.stopAnimation();
    progress.setValue(1);
    if (enabled && motionKey !== undefined && enter) {
      progress.setValue(0);
      Animated.timing(progress, {
        toValue: 1,
        duration: 180,
        useNativeDriver: true,
      }).start();
    }
    return () => {
      progress.stopAnimation();
      progress.setValue(1);
    };
  }, [enabled, motionKey, animateOnMount, progress]);
  return progress;
}

export function Icon({
  name,
  size = 24,
  color,
}: {
  name: IconName;
  size?: number;
  color?: string;
}) {
  const c = usePalette();
  const brand =
    name === "yandex" || name === "vk" || name === "max" || name === "telegram";
  return (
    <View
      style={{
        width: size,
        height: size,
        flexShrink: 0,
        overflow: "hidden",
        borderRadius: name === "yandex" || name === "vk" ? size * 0.25 : 0,
        alignItems: "center",
        justifyContent: "center",
      }}
    >
      <Image
        accessible={false}
        source={iconSources[name]}
        style={{
          // VK's source has 12 px transparent padding around its 72 px mark.
          width: name === "vk" ? size * (96 / 72) : size,
          height: name === "vk" ? size * (96 / 72) : size,
          resizeMode: "contain",
          ...(brand ? {} : { tintColor: color ?? c.accentText }),
        }}
      />
    </View>
  );
}

export function BrandHeader({ centered = false }: { centered?: boolean }) {
  const c = usePalette();
  return (
    <View style={[styles.brand, centered ? styles.brandCentered : null]}>
      <GlitchAccent style={styles.brandLogo}>
        <Image
          accessible={false}
          source={require("../assets/logo.png")}
          style={styles.brandLogo}
        />
      </GlitchAccent>
      <GlitchAccent style={{ flexShrink: 1 }}>
        <Text
          accessibilityLabel="Heartbeat Checker"
          style={[styles.brandText, { color: c.text }]}
        >
          <Text style={styles.brandStrong}>Heartbeat</Text>
          <Text style={styles.brandLight}> Checker</Text>
        </Text>
      </GlitchAccent>
    </View>
  );
}

export function usePalette() {
  const dark = useColorScheme() === "dark";
  return useMemo(
    () => ({
      dark,
      bg: dark ? "#101d20" : "#f5f9f8",
      card: dark ? "#18292c" : "#ffffff",
      glass: dark ? "rgba(25,42,50,0.88)" : "rgba(255,255,255,0.86)",
      glassBorder: dark ? "rgba(154,214,209,0.24)" : "rgba(255,255,255,0.94)",
      glassHighlight: dark
        ? "rgba(210,250,246,0.045)"
        : "rgba(255,255,255,0.32)",
      ambientOne: dark ? "rgba(32,126,117,0.17)" : "rgba(118,210,195,0.15)",
      ambientTwo: dark ? "rgba(43,82,138,0.15)" : "rgba(155,187,228,0.13)",
      text: dark ? "#e8f5f2" : "#153239",
      subtle: dark ? "#a6c5c0" : "#5b7476",
      border: dark ? "#315258" : "#d6e5e2",
      accent: "#00816f",
      accentText: dark ? "#6cddca" : "#006e60",
      danger: "#c54848",
      dangerText: dark ? "#ff8b84" : "#b63838",
    }),
    [dark],
  );
}

function useReducedTransparency() {
  const [reduced, setReduced] = useState(true);
  useEffect(() => {
    let mounted = true;
    void AccessibilityInfo.isReduceTransparencyEnabled()
      .then((value) => {
        if (mounted) setReduced(value);
      })
      .catch(() => undefined);
    const subscription = AccessibilityInfo.addEventListener(
      "reduceTransparencyChanged",
      setReduced,
    );
    return () => {
      mounted = false;
      subscription.remove();
    };
  }, []);
  return reduced;
}

const ReducedTransparency = createContext(true);

export function useGlassEnabled() {
  return !useContext(ReducedTransparency);
}

export function GlassPreferences({ children }: { children: React.ReactNode }) {
  const reduced = useReducedTransparency();
  return (
    <ReducedTransparency.Provider value={reduced}>
      {children}
    </ReducedTransparency.Provider>
  );
}

export function GlassBackdrop() {
  const c = usePalette();
  const reduced = useContext(ReducedTransparency);
  const enabled = useMotionEnabled();
  const drift = useRef(new Animated.Value(0)).current;
  useEffect(() => {
    drift.stopAnimation();
    drift.setValue(0);
    if (!enabled || reduced) return;
    const animation = Animated.loop(
      Animated.sequence([
        Animated.timing(drift, {
          toValue: 1,
          duration: 11000,
          useNativeDriver: true,
          isInteraction: false,
        }),
        Animated.timing(drift, {
          toValue: 0,
          duration: 11000,
          useNativeDriver: true,
          isInteraction: false,
        }),
      ]),
    );
    animation.start();
    return () => {
      animation.stop();
      drift.stopAnimation();
      drift.setValue(0);
    };
  }, [drift, enabled, reduced]);
  if (reduced) return null;
  return (
    <View pointerEvents="none" style={StyleSheet.absoluteFill}>
      <Animated.View
        style={[
          styles.ambientOne,
          {
            backgroundColor: c.ambientOne,
            transform: [
              {
                translateX: drift.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0, 18],
                }),
              },
              {
                translateY: drift.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0, 12],
                }),
              },
            ],
          },
        ]}
      />
      <Animated.View
        style={[
          styles.ambientTwo,
          {
            backgroundColor: c.ambientTwo,
            transform: [
              {
                translateX: drift.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0, -14],
                }),
              },
              {
                translateY: drift.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0, -18],
                }),
              },
            ],
          },
        ]}
      />
    </View>
  );
}

export function Page({
  children,
  title,
  refresh,
  motionKey,
}: {
  children: React.ReactNode;
  title?: string;
  refresh?: () => Promise<unknown> | void;
  motionKey?: string;
}) {
  const c = usePalette();
  const progress = useEntryMotion(motionKey);
  const [refreshing, setRefreshing] = useState(false);
  const refreshPending = useRef(false);
  const onRefresh = useCallback(() => {
    if (!refresh || refreshPending.current) return;
    refreshPending.current = true;
    setRefreshing(true);
    void Promise.resolve()
      .then(refresh)
      .catch(() => undefined)
      .finally(() => {
        refreshPending.current = false;
        setRefreshing(false);
      });
  }, [refresh]);
  return (
    <KeyboardAvoidingView
      behavior="height"
      style={{ flex: 1, backgroundColor: c.bg }}
    >
      <GlassBackdrop />
      <Animated.ScrollView
        keyboardShouldPersistTaps="handled"
        style={{
          flex: 1,
          opacity: progress.interpolate({
            inputRange: [0, 1],
            outputRange: [0.8, 1],
          }),
          transform: [
            {
              translateY: progress.interpolate({
                inputRange: [0, 1],
                outputRange: [6, 0],
              }),
            },
          ],
        }}
        contentContainerStyle={styles.page}
        refreshControl={
          refresh ? (
            <RefreshControl
              refreshing={refreshing}
              onRefresh={onRefresh}
              tintColor={c.accentText}
              colors={[c.accentText]}
            />
          ) : undefined
        }
      >
        <BrandHeader />
        {title ? (
          <View style={styles.header}>
            <Text
              accessibilityRole="header"
              style={[styles.title, { color: c.text }]}
            >
              {title}
            </Text>
          </View>
        ) : null}
        {children}
      </Animated.ScrollView>
    </KeyboardAvoidingView>
  );
}
export function Card({
  children,
  style,
}: {
  children: React.ReactNode;
  style?: StyleProp<ViewStyle>;
}) {
  const c = usePalette();
  const reduced = useContext(ReducedTransparency);
  return (
    <View
      style={[
        styles.card,
        {
          backgroundColor: reduced ? c.card : c.glass,
          borderColor: reduced ? c.border : c.glassBorder,
          shadowColor: c.dark ? "#000000" : "#30565d",
        },
        style,
      ]}
    >
      {!reduced ? (
        <View
          pointerEvents="none"
          style={[styles.cardHighlight, { backgroundColor: c.glassHighlight }]}
        />
      ) : null}
      {children}
    </View>
  );
}
export function Label({
  children,
  muted = false,
}: {
  children: React.ReactNode;
  muted?: boolean;
}) {
  const c = usePalette();
  return (
    <Text style={[styles.label, { color: muted ? c.subtle : c.text }]}>
      {children}
    </Text>
  );
}
export function Title({ children }: { children: React.ReactNode }) {
  const c = usePalette();
  return (
    <Text
      accessibilityRole="header"
      style={[styles.subtitle, { color: c.text }]}
    >
      {children}
    </Text>
  );
}
export function Field({
  label,
  value,
  onChangeText,
  secure,
  keyboardType,
  placeholder,
  multiline,
  hint,
  error,
  autoComplete,
  textContentType,
  returnKeyType,
  onSubmitEditing,
  maxLength,
  editable = true,
}: {
  label: string;
  value: string;
  onChangeText: (value: string) => void;
  secure?: boolean;
  keyboardType?: "email-address" | "numeric" | "number-pad" | "url";
  placeholder?: string;
  multiline?: boolean;
  hint?: string;
  error?: string;
  autoComplete?: TextInputProps["autoComplete"];
  textContentType?: TextInputProps["textContentType"];
  returnKeyType?: TextInputProps["returnKeyType"];
  onSubmitEditing?: () => void;
  maxLength?: number;
  editable?: boolean;
}) {
  const c = usePalette();
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [focused, setFocused] = useState(false);
  return (
    <View style={styles.field}>
      <Label>{label}</Label>
      <View style={styles.inputWrap}>
        <TextInput
          accessibilityLabel={label}
          accessibilityHint={error || hint}
          style={[
            styles.input,
            secure ? styles.passwordInput : null,
            {
              color: c.text,
              backgroundColor: c.card,
              borderColor: error
                ? c.dangerText
                : focused
                  ? c.accentText
                  : c.border,
              opacity: editable ? 1 : 0.6,
            },
          ]}
          value={value}
          onChangeText={onChangeText}
          secureTextEntry={secure && !passwordVisible}
          keyboardType={keyboardType}
          autoCapitalize="none"
          autoCorrect={false}
          placeholder={placeholder}
          placeholderTextColor={c.subtle}
          multiline={multiline}
          autoComplete={autoComplete}
          textContentType={textContentType}
          returnKeyType={returnKeyType}
          onSubmitEditing={onSubmitEditing}
          maxLength={maxLength}
          editable={editable}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
        />
        {secure ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={
              passwordVisible ? "Скрыть пароль" : "Показать пароль"
            }
            accessibilityState={{ selected: passwordVisible }}
            disabled={!editable}
            onPress={() => setPasswordVisible((visible) => !visible)}
            style={styles.passwordToggle}
          >
            <View style={[styles.eye, { borderColor: c.subtle }]}>
              <View style={[styles.pupil, { backgroundColor: c.subtle }]} />
            </View>
            {passwordVisible ? (
              <View style={[styles.eyeSlash, { backgroundColor: c.subtle }]} />
            ) : null}
          </Pressable>
        ) : null}
      </View>
      {error || hint ? (
        <Text
          accessibilityLiveRegion={error ? "polite" : undefined}
          style={[styles.fieldHint, { color: error ? c.dangerText : c.subtle }]}
        >
          {error || hint}
        </Text>
      ) : null}
    </View>
  );
}
export function Button({
  label,
  onPress,
  disabled,
  secondary,
  danger,
  icon,
  iconName,
  loading = false,
  loadingLabel,
  accessibilityLabel,
  selected,
  style,
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  secondary?: boolean;
  danger?: boolean;
  icon?: React.ReactNode;
  iconName?: IconName;
  loading?: boolean;
  loadingLabel?: string;
  accessibilityLabel?: string;
  selected?: boolean;
  style?: StyleProp<ViewStyle>;
}) {
  const c = usePalette();
  const reduced = useContext(ReducedTransparency);
  const [focused, setFocused] = useState(false);
  const [pressed, setPressed] = useState(false);
  const motionEnabled = useMotionEnabled();
  const scale = useRef(new Animated.Value(1)).current;
  const reset = useCallback(() => {
    scale.stopAnimation();
    scale.setValue(1);
    setPressed(false);
  }, [scale]);
  useEffect(() => {
    reset();
    return () => {
      scale.stopAnimation();
      scale.setValue(1);
    };
  }, [motionEnabled, disabled, loading, reset, scale]);
  const press = (down: boolean) => {
    setPressed(down);
    scale.stopAnimation();
    if (!motionEnabled || disabled || loading) {
      scale.setValue(1);
      return;
    }
    Animated.timing(scale, {
      toValue: down ? 0.99 : 1,
      duration: down ? 80 : 120,
      useNativeDriver: true,
    }).start();
  };
  return (
    <AnimatedPressable
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel}
      accessibilityState={{
        disabled: !!disabled || loading,
        busy: loading,
        selected,
      }}
      disabled={disabled || loading}
      onPress={onPress}
      onPressIn={() => press(true)}
      onPressOut={() => press(false)}
      onFocus={() => setFocused(true)}
      onBlur={() => {
        setFocused(false);
        reset();
      }}
      style={[
        styles.button,
        {
          backgroundColor: danger
            ? c.danger
            : secondary
              ? reduced
                ? c.card
                : c.glass
              : c.accent,
          borderColor: focused
            ? c.text
            : danger
              ? c.danger
              : secondary
                ? reduced
                  ? c.border
                  : c.glassBorder
                : c.accent,
          opacity: disabled && !loading ? 0.5 : pressed ? 0.76 : 1,
          transform: [{ scale }],
        },
        style,
      ]}
    >
      <View style={styles.buttonContent}>
        {loading ? (
          <ActivityIndicator color={secondary ? c.accentText : "#fff"} />
        ) : (
          (icon ??
          (iconName ? (
            <Icon name={iconName} color={secondary ? c.accentText : "#fff"} />
          ) : null))
        )}
        <Text
          style={[
            styles.buttonText,
            { color: secondary ? c.accentText : "#fff" },
          ]}
        >
          {loading ? (loadingLabel ?? label) : label}
        </Text>
      </View>
    </AnimatedPressable>
  );
}
export function ErrorNotice({ message }: { message: string }) {
  return message ? <Notice message={message} tone="danger" /> : null;
}
export function Notice({
  message,
  tone = "info",
}: {
  message: string;
  tone?: "info" | "danger" | "success";
}) {
  const c = usePalette();
  const color = tone === "danger" ? c.dangerText : c.accentText;
  return (
    <View
      style={[
        styles.noticeBox,
        {
          backgroundColor: c.card,
          borderColor: c.border,
          borderLeftColor: color,
        },
      ]}
    >
      <Icon
        name={
          tone === "danger" ? "alert" : tone === "success" ? "check" : "info"
        }
        color={color}
        size={20}
      />
      <Text
        accessibilityRole={tone === "danger" ? "alert" : undefined}
        accessibilityLiveRegion="polite"
        style={[styles.notice, { color: tone === "danger" ? color : c.text }]}
      >
        {message}
      </Text>
    </View>
  );
}
export function DetailRow({ label, value }: { label: string; value: string }) {
  const c = usePalette();
  return (
    <View style={[styles.detailRow, { borderBottomColor: c.border }]}>
      <Text style={[styles.detailLabel, { color: c.subtle }]}>{label}</Text>
      <Text selectable style={[styles.detailValue, { color: c.text }]}>
        {value}
      </Text>
    </View>
  );
}
export function TextAction({
  label,
  onPress,
  disabled = false,
  iconName,
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  iconName?: IconName;
}) {
  const c = usePalette();
  const [focused, setFocused] = useState(false);
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled }}
      disabled={disabled}
      onPress={onPress}
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      style={({ pressed }) => [
        styles.textAction,
        {
          opacity: disabled ? 0.5 : pressed ? 0.65 : 1,
          borderColor: focused ? c.accentText : "transparent",
        },
      ]}
    >
      {iconName ? <Icon name={iconName} size={20} /> : null}
      <Text style={[styles.textActionLabel, { color: c.accentText }]}>
        {label}
      </Text>
    </Pressable>
  );
}
export function Spacer() {
  return <View style={{ height: 12 }} />;
}
export const styles = StyleSheet.create({
  page: {
    padding: 18,
    paddingBottom: 42,
    gap: 14,
    width: "100%",
    maxWidth: 760,
    alignSelf: "center",
  },
  brand: { flexDirection: "row", alignItems: "center", gap: 9, minHeight: 34 },
  brandCentered: { justifyContent: "center" },
  brandLogo: { width: 32, height: 32, borderRadius: 9 },
  brandText: { fontSize: 18, letterSpacing: -0.5, flexShrink: 1 },
  brandStrong: { fontWeight: "800" },
  brandLight: { fontWeight: "500" },
  glitchLine: { position: "absolute", left: 0, right: 0, height: 2 },
  signal: {
    height: 64,
    borderWidth: 1,
    borderRadius: 13,
    overflow: "hidden",
    justifyContent: "center",
    paddingHorizontal: 18,
  },
  signalDot: { width: 7, height: 7, borderRadius: 4 },
  signalWave: {
    position: "absolute",
    width: 136,
    height: 38,
    alignSelf: "center",
  },
  signalLine: { position: "absolute", height: 2, borderRadius: 1 },
  signalGrid: {
    position: "absolute",
    right: 18,
    width: 18,
    height: 18,
    borderWidth: 1,
    borderRadius: 5,
  },
  header: { width: "100%" },
  title: { fontSize: 24, lineHeight: 31, fontWeight: "700", flexShrink: 1 },
  subtitle: {
    fontSize: 19,
    lineHeight: 26,
    fontWeight: "700",
    marginBottom: 2,
  },
  card: {
    borderWidth: 1,
    borderRadius: 16,
    padding: 16,
    gap: 10,
    elevation: 2,
    shadowOffset: { width: 0, height: 5 },
    shadowOpacity: 0.1,
    shadowRadius: 13,
  },
  cardHighlight: {
    position: "absolute",
    top: 1,
    left: 1,
    right: 1,
    height: 34,
    borderTopLeftRadius: 15,
    borderTopRightRadius: 15,
  },
  ambientOne: {
    position: "absolute",
    width: 300,
    height: 300,
    borderRadius: 150,
    top: -115,
    left: -120,
  },
  ambientTwo: {
    position: "absolute",
    width: 360,
    height: 360,
    borderRadius: 180,
    top: 165,
    right: -155,
  },
  label: { fontSize: 15, lineHeight: 22 },
  field: { gap: 5 },
  inputWrap: { position: "relative" },
  input: {
    borderWidth: 1.5,
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: 11,
    fontSize: 16,
    minHeight: 48,
  },
  passwordInput: { paddingRight: 58 },
  passwordToggle: {
    position: "absolute",
    right: 0,
    top: 0,
    bottom: 0,
    width: 48,
    minHeight: 48,
    alignItems: "center",
    justifyContent: "center",
  },
  eye: {
    width: 22,
    height: 14,
    borderWidth: 2,
    borderRadius: 12,
    alignItems: "center",
    justifyContent: "center",
  },
  pupil: { width: 7, height: 7, borderRadius: 4 },
  eyeSlash: {
    width: 28,
    height: 2,
    position: "absolute",
    transform: [{ rotate: "-32deg" }],
  },
  button: {
    minHeight: 48,
    paddingHorizontal: 16,
    paddingVertical: 11,
    alignItems: "center",
    justifyContent: "center",
    borderRadius: 10,
    borderWidth: 1,
  },
  buttonText: {
    fontSize: 16,
    fontWeight: "600",
    textAlign: "center",
    flexShrink: 1,
  },
  buttonContent: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    gap: 9,
    maxWidth: "100%",
  },
  notice: { fontSize: 14, lineHeight: 21, flex: 1 },
  noticeBox: {
    flexDirection: "row",
    alignItems: "flex-start",
    gap: 9,
    padding: 12,
    borderWidth: 1,
    borderLeftWidth: 3,
    borderRadius: 10,
  },
  fieldHint: { fontSize: 13, lineHeight: 19 },
  detailRow: {
    flexDirection: "row",
    flexWrap: "wrap",
    justifyContent: "space-between",
    gap: 7,
    paddingVertical: 10,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  detailLabel: { fontSize: 14, lineHeight: 21, flexGrow: 1 },
  detailValue: {
    fontSize: 14,
    lineHeight: 21,
    fontWeight: "600",
    flexShrink: 1,
  },
  textAction: {
    minWidth: 48,
    minHeight: 48,
    paddingVertical: 10,
    paddingHorizontal: 4,
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    gap: 8,
    borderWidth: 1,
    borderRadius: 8,
  },
  textActionLabel: {
    fontSize: 14,
    lineHeight: 21,
    fontWeight: "600",
    textAlign: "center",
    flexShrink: 1,
  },
});
