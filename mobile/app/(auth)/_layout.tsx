import { Stack } from 'expo-router';

export default function AuthLayout() {
  return (
    <Stack screenOptions={{ headerShown: false }}>
      <Stack.Screen name="server" />
      <Stack.Screen name="auth-email" />
      <Stack.Screen name="relay-desktops" />
      <Stack.Screen name="pair-scan" />
      <Stack.Screen name="pair-waiting" />
    </Stack>
  );
}
