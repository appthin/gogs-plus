import { useTranslation } from "react-i18next";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { subUrl } from "@/lib/url";

export function Captcha({
  inputRef,
  value,
  onChange,
  onRefresh,
  refreshKey,
  disabled,
  invalid,
  describedBy,
  imageTabIndex,
  inputTabIndex,
  children,
}: {
  inputRef: React.RefObject<HTMLInputElement | null>;
  value: string;
  onChange: (value: string) => void;
  onRefresh: () => void;
  refreshKey: number;
  disabled: boolean;
  invalid: boolean;
  describedBy?: string;
  imageTabIndex: number;
  inputTabIndex: number;
  children?: React.ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor="captcha">{t("captcha")}</Label>
      <div className="group relative">
        <button
          type="button"
          tabIndex={imageTabIndex}
          disabled={disabled}
          onClick={onRefresh}
          aria-label={t("refresh_captcha")}
          className="block w-full cursor-pointer overflow-hidden rounded-md border border-(--color-border) bg-(--color-surface) outline-none focus-visible:ring-1 focus-visible:ring-(--color-ring) disabled:cursor-not-allowed disabled:opacity-60"
        >
          <img
            src={subUrl("/captcha/image.jpeg") + "?refresh=true&v=" + refreshKey}
            alt={t("captcha_image_alt")}
            className="block h-20 w-full object-fill"
          />
        </button>
        <span
          role="tooltip"
          className="pointer-events-none absolute top-1/2 left-1/2 z-10 -translate-x-1/2 -translate-y-1/2 rounded-md bg-(--color-foreground) px-2 py-1 text-xs font-medium text-(--color-background) opacity-0 shadow transition-opacity duration-150 group-hover:opacity-90 group-focus-within:opacity-90"
        >
          {t("click_to_refresh_captcha")}
        </span>
      </div>
      <Input
        ref={inputRef}
        id="captcha"
        name="captcha"
        type="text"
        autoComplete="off"
        required
        tabIndex={inputTabIndex}
        placeholder={t("captcha_placeholder")}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={invalid ? true : undefined}
        aria-describedby={describedBy}
      />
      {children}
    </div>
  );
}
