import { useEffect, useRef } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNotificationWSStore } from '@/stores/notification-ws-store';
import { dispatchNotification } from '@/lib/notification-dispatcher';

export function useNotificationWS() {
  const queryClient = useQueryClient();
  const lastMessage = useNotificationWSStore((s) => s.lastMessage);
  const reconnectCount = useNotificationWSStore((s) => s.reconnectCount);
  const connect = useNotificationWSStore((s) => s.connect);
  const disconnect = useNotificationWSStore((s) => s.disconnect);
  const prevReconnectCount = useRef(0);

  useEffect(() => {
    connect();
    return () => disconnect();
  }, [connect, disconnect]);

  useEffect(() => {
    if (lastMessage) {
      dispatchNotification(queryClient, lastMessage);
    }
  }, [lastMessage, queryClient]);

  useEffect(() => {
    if (reconnectCount > 0 && reconnectCount !== prevReconnectCount.current) {
      prevReconnectCount.current = reconnectCount;
      queryClient.invalidateQueries();
    }
  }, [reconnectCount, queryClient]);
}
