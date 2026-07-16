import { FaAws } from "react-icons/fa";
import { RiOpenaiFill } from "react-icons/ri";
import { SiAnthropic, SiGooglegemini } from "react-icons/si";
import type { IconType } from "react-icons";

const PROVIDER_ICONS: Record<string, { Icon: IconType; className: string }> = {
  openai: { Icon: RiOpenaiFill, className: "text-base-content" },
  anthropic: { Icon: SiAnthropic, className: "text-[#D4A27F]" },
  gemini: { Icon: SiGooglegemini, className: "text-[#4285F4]" },
  bedrock: { Icon: FaAws, className: "text-[#FF9900]" },
  "bedrock-mantle": { Icon: FaAws, className: "text-[#FF9900]" },
};

export function ProviderIcon({
  provider,
  size = 14,
  className = "",
}: {
  provider: string;
  size?: number;
  className?: string;
}) {
  const entry = PROVIDER_ICONS[provider];
  if (!entry) {
    return null;
  }
  const { Icon, className: colorClass } = entry;
  return <Icon size={size} className={`${colorClass} ${className}`.trim()} aria-hidden />;
}

/** Badge chip with icon + provider name (tables, headers). */
export function ProviderBadge({ provider }: { provider: string }) {
  return (
    <span className="badge badge-sm badge-outline badge-primary gap-1.5">
      <ProviderIcon provider={provider} size={12} />
      {provider}
    </span>
  );
}

/** Inline icon + name without badge chrome (selects, sentences). */
export function ProviderLabel({
  provider,
  className = "",
}: {
  provider: string;
  className?: string;
}) {
  return (
    <span className={`inline-flex items-center gap-1.5 ${className}`.trim()}>
      <ProviderIcon provider={provider} size={14} />
      <span>{provider}</span>
    </span>
  );
}

/** Native select with a live icon for the current value. */
export function ProviderSelect({
  value,
  onChange,
  options,
  disabled,
  className = "select select-bordered w-full",
  emptyLabel,
}: {
  value: string;
  onChange: (value: string) => void;
  options: string[];
  disabled?: boolean;
  className?: string;
  /** When set, adds an empty option (e.g. "All providers"). */
  emptyLabel?: string;
}) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      {value ? <ProviderIcon provider={value} size={16} className="shrink-0" /> : null}
      <select
        className={`${className} min-w-0 flex-1`.trim()}
        disabled={disabled}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      >
        {emptyLabel !== undefined ? <option value="">{emptyLabel}</option> : null}
        {options.map((provider) => (
          <option key={provider} value={provider}>
            {provider}
          </option>
        ))}
      </select>
    </div>
  );
}
