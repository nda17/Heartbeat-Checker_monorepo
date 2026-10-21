import React, { useMemo } from "react";
import {
  KeyboardAvoidingView,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  useColorScheme,
  View,
} from "react-native";

export function usePalette() {
  const dark = useColorScheme() === "dark";
  return useMemo(
    () => ({
      dark,
      bg: dark ? "#101d20" : "#f5f9f8",
      card: dark ? "#18292c" : "#ffffff",
      text: dark ? "#e8f5f2" : "#153239",
      subtle: dark ? "#a6c5c0" : "#5b7476",
      border: dark ? "#315258" : "#d6e5e2",
      accent: "#00816f",
      danger: "#c54848",
    }),
    [dark],
  );
}

export function Page({
  children,
  title,
  refresh,
}: {
  children: React.ReactNode;
  title?: string;
  refresh?: () => void;
}) {
  const c = usePalette();
  return (
    <KeyboardAvoidingView
      behavior="height"
      style={{ flex: 1, backgroundColor: c.bg }}
    >
      <ScrollView
        keyboardShouldPersistTaps="handled"
        style={{ flex: 1 }}
        contentContainerStyle={styles.page}
      >
        {title ? (
          <View style={styles.header}>
            <Text
              accessibilityRole="header"
              style={[styles.title, { color: c.text }]}
            >
              {title}
            </Text>
            {refresh ? (
              <Button label="Обновить" onPress={refresh} secondary />
            ) : null}
          </View>
        ) : null}
        {children}
      </ScrollView>
    </KeyboardAvoidingView>
  );
}
export function Card({ children }: { children: React.ReactNode }) {
  const c = usePalette();
  return (
    <View
      style={[styles.card, { backgroundColor: c.card, borderColor: c.border }]}
    >
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
  return (
    <View style={styles.field}>
      <Label>{label}</Label>
      <TextInput
        accessibilityLabel={label}
        style={[
          styles.input,
          { color: c.text, backgroundColor: c.card, borderColor: c.border },
        ]}
        value={value}
        onChangeText={onChangeText}
        secureTextEntry={secure}
        keyboardType={keyboardType}
        autoCapitalize="none"
        autoCorrect={false}
        placeholder={placeholder}
        placeholderTextColor={c.subtle}
        multiline={multiline}
      />
    </View>
  );
}
export function Button({
  label,
  onPress,
  disabled,
  secondary,
  danger,
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  secondary?: boolean;
  danger?: boolean;
}) {
  const c = usePalette();
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled: !!disabled }}
      disabled={disabled}
      onPress={onPress}
      style={[
        styles.button,
        {
          backgroundColor: danger ? c.danger : secondary ? c.card : c.accent,
          borderColor: danger ? c.danger : c.accent,
          opacity: disabled ? 0.5 : 1,
        },
      ]}
    >
      <Text
        style={[styles.buttonText, { color: secondary ? c.accent : "#fff" }]}
      >
        {label}
      </Text>
    </Pressable>
  );
}
export function ErrorNotice({ message }: { message: string }) {
  const c = usePalette();
  return message ? (
    <Text
      accessibilityRole="alert"
      style={[styles.notice, { color: c.danger }]}
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
  header: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 12,
  },
  title: { fontSize: 27, fontWeight: "700", flex: 1 },
  subtitle: { fontSize: 19, fontWeight: "600", marginBottom: 8 },
  card: { borderWidth: 1, borderRadius: 16, padding: 16, gap: 10 },
  label: { fontSize: 15, lineHeight: 22 },
  field: { gap: 5 },
  input: {
    borderWidth: 1,
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: 11,
    fontSize: 16,
    minHeight: 48,
  },
  button: {
    minHeight: 46,
    paddingHorizontal: 16,
    paddingVertical: 11,
    alignItems: "center",
    justifyContent: "center",
    borderRadius: 10,
    borderWidth: 1,
  },
  buttonText: { fontSize: 16, fontWeight: "600", textAlign: "center" },
  notice: { fontSize: 14, lineHeight: 20 },
});
