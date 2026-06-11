import type { AdminUser } from "../types";

export function avatarUrl(user: AdminUser | undefined): string | undefined {
  if (!user) return undefined;
  if (user.picture) return user.picture;
  const label = user.name || user.email?.split("@")[0];
  if (!label) return undefined;
  return `https://ui-avatars.com/api/?name=${encodeURIComponent(label)}&background=6366f1&color=fff&size=128&bold=true`;
}

interface UserAvatarProps {
  user: AdminUser | undefined;
  size?: "sm" | "md";
  className?: string;
}

const sizeClass = {
  sm: "h-9 w-9",
  md: "h-10 w-10",
} as const;

export default function UserAvatar({ user, size = "md", className = "" }: UserAvatarProps) {
  const src = avatarUrl(user);
  const initials = user?.email?.slice(0, 1).toUpperCase() ?? "?";

  return (
    <div className={`avatar ${className}`.trim()}>
      <div className={`${sizeClass[size]} rounded-full ring-1 ring-base-300/80`}>
        {src ? (
          <img src={src} alt="" referrerPolicy="no-referrer" className="rounded-full object-cover" />
        ) : (
          <div
            className={`flex ${sizeClass[size]} items-center justify-center rounded-full bg-primary/10 text-primary`}
          >
            <span className="text-sm font-semibold">{initials}</span>
          </div>
        )}
      </div>
    </div>
  );
}
