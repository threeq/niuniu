export function VSCodeIcon({ className = 'w-4 h-4' }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
      {/* VS Code blue background */}
      <path
        fill="#007ACC"
        d="M2 2h20v20L12 19L2 22V2z"
      />
      {/* Left dark section */}
      <path
        fill="#1E1E1E"
        d="M2 2l10 3 10-3v17l-10-3-10 3V2z"
      />
      {/* V letter */}
      <path
        fill="#FFFFFF"
        d="M5 5l5 10 5-10 1.5 2h-1l1 1.5L12 15l-5-6.5L5 5z"
      />
      {/* S letter */}
      <path
        fill="#FFFFFF"
        d="M15.5 7c-.8 0-1.5.3-2 1-.3.5-.5 1-.5 1.5 0 1 .7 1.5 1.5 2 .5.3 1 .7 1 1.2 0 .5-.4.8-1 1-.8.2-1.5.2-2 .2v1.5c.6 0 1.3-.1 2-.3 1.2-.4 2-1.3 2-2.5 0-.8-.3-1.5-.8-2 .4-.5.7-1 .7-1.7 0-1.2-1-2-2-2.5v1.6z"
      />
    </svg>
  );
}
