import React from 'react';
import { Tabs } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import { useThemeColors } from '../../src/theme/useTheme';

export default function TabLayout() {
  const colors = useThemeColors();

  return (
    <Tabs
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: colors.brand.accent,
        tabBarInactiveTintColor: colors.text.tertiary,
        tabBarStyle: {
          backgroundColor: colors.bg.base,
          borderTopColor: colors.border.default,
          borderTopWidth: 1,
        },
        tabBarLabelStyle: {
          fontSize: 10,
          fontWeight: '500',
        },
      }}
    >
      <Tabs.Screen
        name="dashboard"
        options={{
          title: '首页',
          tabBarIcon: ({ color, size }) => (
            <Ionicons name="home-outline" size={size - 2} color={color} />
          ),
        }}
      />
      <Tabs.Screen
        name="projects"
        options={{
          title: '项目',
          tabBarIcon: ({ color, size }) => (
            <Ionicons name="folder-outline" size={size - 2} color={color} />
          ),
        }}
      />
      <Tabs.Screen
        name="workspaces"
        options={{
          title: '工作空间',
          tabBarIcon: ({ color, size }) => (
            <Ionicons name="cube-outline" size={size - 2} color={color} />
          ),
        }}
      />
      <Tabs.Screen
        name="repositories"
        options={{
          title: '仓库',
          tabBarIcon: ({ color, size }) => (
            <Ionicons name="git-branch-outline" size={size - 2} color={color} />
          ),
        }}
      />
      <Tabs.Screen
        name="inbox"
        options={{
          href: null,
        }}
      />
    </Tabs>
  );
}
