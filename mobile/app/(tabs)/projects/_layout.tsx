import { Stack } from 'expo-router';

// Project detail page lives at /project/[id] (root route, not under tabs)
// so navigation between projects accumulates in the root stack and the
// back button traces the actual visit history. Tab here only contains the
// list page; tapping a project from the list pushes a root-level detail
// page (matching the workspace/[id] and repository/[id] pattern).
export default function ProjectsStackLayout() {
  return (
    <Stack screenOptions={{ headerShown: false }}>
      <Stack.Screen name="index" />
    </Stack>
  );
}
