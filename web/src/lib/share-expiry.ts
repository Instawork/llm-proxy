export function formatShareExpiry(expiresAt: string): { message: string; urgent: boolean } {
  const exp = new Date(expiresAt);
  if (Number.isNaN(exp.getTime())) {
    return { message: "Expiry time unavailable", urgent: false };
  }

  const ms = exp.getTime() - Date.now();
  const formatted = exp.toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });

  if (ms <= 0) {
    return { message: `This link expired on ${formatted}`, urgent: true };
  }

  const hours = ms / (60 * 60 * 1000);
  if (hours < 1) {
    return {
      message: `This link expires in under an hour (${formatted})`,
      urgent: true,
    };
  }
  if (hours < 6) {
    const rounded = Math.ceil(hours);
    return {
      message: `This link expires in about ${rounded} hour${rounded === 1 ? "" : "s"} (${formatted})`,
      urgent: true,
    };
  }
  if (hours < 24) {
    const rounded = Math.ceil(hours);
    return {
      message: `This link expires in about ${rounded} hours (${formatted})`,
      urgent: false,
    };
  }

  return { message: `This link expires on ${formatted}`, urgent: false };
}
