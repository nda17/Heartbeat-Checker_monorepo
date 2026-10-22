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
  Animated,
  AppState,
  Image,
  KeyboardAvoidingView,
  Pressable,
  RefreshControl,
  ScrollView,
  StyleProp,
  StyleSheet,
  Text,
  TextInput,
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
    <Image
      accessible={false}
      source={iconSources[name]}
      style={{
        width: size,
        height: size,
        resizeMode: "contain",
        ...(brand ? {} : { tintColor: color ?? c.accentText }),
      }}
    />
  );
}

export function BrandHeader({ centered = false }: { centered?: boolean }) {
  const c = usePalette();
  const offset = useRef(new Animated.Value(0)).current;
  const [reducedMotion, setReducedMotion] = useState<boolean | null>(null);
  const [active, setActive] = useState(AppState.currentState === "active");
  useEffect(() => {
    let mounted = true;
    void AccessibilityInfo.isReduceMotionEnabled()
      .then((value) => {
        if (mounted) setReducedMotion(value);
      })
      .catch(() => {
        if (mounted) setReducedMotion(true);
      });
    const motion = AccessibilityInfo.addEventListener(
      "reduceMotionChanged",
      setReducedMotion,
    );
    const app = AppState.addEventListener("change", (state) => {
      setActive(state === "active");
    });
    return () => {
      mounted = false;
      motion.remove();
      app.remove();
    };
  }, []);
  useEffect(() => {
    if (reducedMotion !== false || !active) return;
    const timer = setInterval(() => {
      Animated.sequence([
        Animated.timing(offset, {
          toValue: -1,
          duration: 70,
          useNativeDriver: true,
        }),
        Animated.timing(offset, {
          toValue: 1,
          duration: 70,
          useNativeDriver: true,
        }),
        Animated.timing(offset, {
          toValue: 0,
          duration: 70,
          useNativeDriver: true,
        }),
      ]).start();
    }, 6000);
    return () => {
      clearInterval(timer);
      offset.stopAnimation();
      offset.setValue(0);
    };
  }, [active, offset, reducedMotion]);
  return (
    <View style={[styles.brand, centered ? styles.brandCentered : null]}>
      <Image
        accessible={false}
        source={require("../assets/logo.png")}
        style={styles.brandLogo}
      />
      <Animated.Text
        accessibilityLabel="Heartbeat Checker"
        style={[
          styles.brandText,
          { color: c.text, transform: [{ translateX: offset }] },
        ]}
      >
        <Text style={styles.brandStrong}>Heartbeat</Text>
        <Text style={styles.brandLight}> Checker</Text>
      </Animated.Text>
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
  if (reduced) return null;
  return (
    <View pointerEvents="none" style={StyleSheet.absoluteFill}>
      <View style={[styles.ambientOne, { backgroundColor: c.ambientOne }]} />
      <View style={[styles.ambientTwo, { backgroundColor: c.ambientTwo }]} />
    </View>
  );
}

export function Page({
  children,
  title,
  refresh,
}: {
  children: React.ReactNode;
  title?: string;
  refresh?: () => Promise<unknown> | void;
}) {
  const c = usePalette();
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
      <ScrollView
        keyboardShouldPersistTaps="handled"
        style={{ flex: 1 }}
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
      </ScrollView>
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
}: {
  label: string;
  value: string;
  onChangeText: (value: string) => void;
  secure?: boolean;
  keyboardType?: "email-address" | "numeric" | "number-pad" | "url";
  placeholder?: string;
  multiline?: boolean;
}) {
  const c = usePalette();
  const [passwordVisible, setPasswordVisible] = useState(false);
  return (
    <View style={styles.field}>
      <Label>{label}</Label>
      <View style={styles.inputWrap}>
        <TextInput
          accessibilityLabel={label}
          style={[
            styles.input,
            secure ? styles.passwordInput : null,
            { color: c.text, backgroundColor: c.card, borderColor: c.border },
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
        />
        {secure ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={
              passwordVisible ? "Скрыть пароль" : "Показать пароль"
            }
            accessibilityState={{ selected: passwordVisible }}
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
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  secondary?: boolean;
  danger?: boolean;
  icon?: React.ReactNode;
  iconName?: IconName;
}) {
  const c = usePalette();
  const reduced = useContext(ReducedTransparency);
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled: !!disabled }}
      disabled={disabled}
      onPress={onPress}
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
          borderColor: danger
            ? c.danger
            : secondary
              ? reduced
                ? c.border
                : c.glassBorder
              : c.accent,
          opacity: disabled ? 0.5 : 1,
        },
      ]}
    >
      <View style={styles.buttonContent}>
        {icon ??
          (iconName ? (
            <Icon name={iconName} color={secondary ? c.accentText : "#fff"} />
          ) : null)}
        <Text
          style={[
            styles.buttonText,
            { color: secondary ? c.accentText : "#fff" },
          ]}
        >
          {label}
        </Text>
      </View>
    </Pressable>
  );
}
export function ErrorNotice({ message }: { message: string }) {
  const c = usePalette();
  return message ? (
    <Text
      accessibilityRole="alert"
      style={[styles.notice, { color: c.dangerText }]}
    >
      {message}
    </Text>
  ) : null;
}
export function Spacer() {
  return <View style={{ height: 12 }} />;
}
export const styles = StyleSheet.create({
  page: { padding: 18, paddingBottom: 42, gap: 14 },
  brand: { flexDirection: "row", alignItems: "center", gap: 9, minHeight: 34 },
  brandCentered: { justifyContent: "center" },
  brandLogo: { width: 32, height: 32, borderRadius: 9 },
  brandText: { fontSize: 18, letterSpacing: -0.5, flexShrink: 1 },
  brandStrong: { fontWeight: "800" },
  brandLight: { fontWeight: "500" },
  header: { width: "100%" },
  title: { fontSize: 24, lineHeight: 31, fontWeight: "700", flexShrink: 1 },
  subtitle: { fontSize: 19, fontWeight: "600", marginBottom: 8 },
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
    borderWidth: 1,
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
  buttonText: { fontSize: 16, fontWeight: "600", textAlign: "center" },
  buttonContent: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    gap: 9,
  },
  notice: { fontSize: 14, lineHeight: 20 },
});
