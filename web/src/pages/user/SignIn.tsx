import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { Eye, EyeOff } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { Captcha } from "@/components/Captcha";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { usePageTitle } from "@/lib/page-title";
import { subUrl } from "@/lib/url";

export interface LoginSource {
  id: number;
  name: string;
  isDefault: boolean;
}

export interface SignInPage {
  loginSources: LoginSource[];
  captchaEnabled: boolean;
}

interface SignInResponse {
  mfa?: boolean;
}

interface SignInErrorResponse {
  error?: string;
  fields?: Record<string, string | null>;
  retryAfter?: number;
}

// Field display order; the first key with a server-side error gets focus.
const FIELD_ORDER = ["username", "password", "captcha"] as const;

// Gogs INI strings carry Go printf placeholders, not i18next `{name}` ones,
// so the remaining seconds are substituted by hand (same as relative-time.ts).
function fillRetryAfter(template: string, seconds: number): string {
  return template.replace(/%(?:\[1\])?s/, String(seconds));
}

const route = getRouteApi("/user/sign-in");

export function SignIn() {
  const { t } = useTranslation();
  usePageTitle(t("sign_in"));
  const navigate = useNavigate();
  const { loginSources, captchaEnabled } = route.useLoaderData();
  const defaultSource = loginSources.find((s) => s.isDefault);

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [loginSource, setLoginSource] = useState<number>(defaultSource?.id ?? 0);
  const [captcha, setCaptcha] = useState("");
  const [captchaRefresh, setCaptchaRefresh] = useState(0);
  const [showPassword, setShowPassword] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [retryAfter, setRetryAfter] = useState(0);
  const [formError, setFormError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string | null>>({});
  const usernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const captchaRef = useRef<HTMLInputElement>(null);
  const countingDown = retryAfter > 0;
  const submitLabel = submitting
    ? t("auth.sign_in_submitting")
    : countingDown
      ? fillRetryAfter(t("auth.sign_in_retry_after"), retryAfter)
      : t("sign_in");

  function refreshCaptcha() {
    setCaptcha("");
    setCaptchaRefresh((value) => value + 1);
  }

  useEffect(() => {
    if (!countingDown) return;
    const id = window.setInterval(() => {
      setRetryAfter((value) => Math.max(0, value - 1));
    }, 1000);
    return () => window.clearInterval(id);
  }, [countingDown]);

  function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (countingDown) return;
    setFormError(null);
    setFieldErrors({});
    setSubmitting(true);
    void (async () => {
      try {
        const res = await fetch(subUrl("/api/web/user/sign-in"), {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ username, password, loginSource, captcha }),
        });
        if (!res.ok) {
          const body = (await res.json().catch(() => ({}))) as SignInErrorResponse;
          if (body.error) setFormError(body.error);
          let focusField: (typeof FIELD_ORDER)[number] | undefined;
          if (body.fields) {
            setFieldErrors(body.fields);
            focusField = FIELD_ORDER.find((f) => f in (body.fields ?? {}));
          }
          if (!body.error && !body.fields) {
            setFormError(t("auth.sign_in_failed"));
          }
          if (res.status === 429) {
            const retry = body.retryAfter ?? Number(res.headers.get("Retry-After"));
            if (Number.isFinite(retry) && retry > 0) setRetryAfter(Math.ceil(retry));
          }
          setSubmitting(false);
          if (captchaEnabled) refreshCaptcha();
          // Defer focus past the React commit so the fieldset is re-enabled
          // (.focus() is a no-op while the field is inside a disabled fieldset).
          requestAnimationFrame(() => {
            if (focusField === "username") usernameRef.current?.focus();
            else if (focusField === "password") passwordRef.current?.focus();
            else if (focusField === "captcha") captchaRef.current?.focus();
          });
          return;
        }
        const data = (await res.json()) as SignInResponse;
        if (data.mfa) {
          // Preserve ?redirect_to= so the MFA step can finalize the same target.
          const search = new URLSearchParams(window.location.search);
          const redirectTo = search.get("redirect_to");
          await navigate({
            to: "/user/mfa",
            search: redirectTo ? { redirect_to: redirectTo } : {},
          });
          return;
        }
        const to = new URLSearchParams(window.location.search).get("redirect_to") ?? "";
        // /redirect is a server endpoint (303), must be a full navigation.
        window.location.assign(subUrl("/redirect") + "?to=" + encodeURIComponent(to));
      } catch {
        setFormError(t("auth.sign_in_failed"));
        setSubmitting(false);
        if (captchaEnabled) refreshCaptcha();
      }
    })();
  }

  return (
    <main className="flex flex-1 items-center justify-center px-4 py-10 sm:px-6 sm:py-16">
      <Card className="w-full max-w-md">
        <CardHeader className="items-center text-center">
          <CardTitle>{t("sign_in")}</CardTitle>
        </CardHeader>
        <CardContent className="pt-2">
          <form onSubmit={onSubmit} noValidate>
            <fieldset disabled={submitting} className="contents">
              {formError && (
                <div
                  role="alert"
                  className="mb-4 rounded-md border border-(--color-destructive) bg-(--color-destructive)/10 px-3 py-2 text-sm text-(--color-destructive)"
                >
                  {formError}
                </div>
              )}

              <div className="flex flex-col gap-4">
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="username">{t("username")}</Label>
                  <Input
                    ref={usernameRef}
                    id="username"
                    name="username"
                    type="text"
                    autoComplete="username"
                    required
                    autoFocus
                    tabIndex={1}
                    placeholder={t("username_placeholder")}
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    aria-invalid={"username" in fieldErrors ? true : undefined}
                    aria-describedby={fieldErrors.username ? "username-error" : undefined}
                  />
                  {fieldErrors.username && (
                    <p id="username-error" className="text-sm text-(--color-destructive)">
                      {fieldErrors.username}
                    </p>
                  )}
                </div>

                <div className="flex flex-col gap-1.5">
                  <div className="flex items-center justify-between gap-3">
                    <Label htmlFor="password">{t("password")}</Label>
                    <Button variant="link" size="inline" asChild>
                      <a
                        href={subUrl("/user/reset-password")}
                        tabIndex={submitting ? -1 : 8}
                        aria-disabled={submitting || undefined}
                        className={submitting ? "pointer-events-none opacity-50" : undefined}
                        onClick={(e) => {
                          if (submitting) e.preventDefault();
                        }}
                      >
                        {t("auth.forget_password")}
                      </a>
                    </Button>
                  </div>
                  <div className="relative">
                    <Input
                      ref={passwordRef}
                      id="password"
                      name="password"
                      type={showPassword ? "text" : "password"}
                      autoComplete="current-password"
                      required
                      tabIndex={2}
                      placeholder={t("password_placeholder")}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      aria-invalid={"password" in fieldErrors ? true : undefined}
                      aria-describedby={fieldErrors.password ? "password-error" : undefined}
                      className="pr-10"
                    />
                    <button
                      type="button"
                      tabIndex={3}
                      disabled={submitting}
                      onClick={() => setShowPassword((v) => !v)}
                      aria-label={showPassword ? t("auth.hide_password") : t("auth.show_password")}
                      aria-pressed={showPassword}
                      className="absolute inset-y-0 right-0 flex w-10 cursor-pointer items-center justify-center rounded-r-md text-(--color-muted-foreground) outline-none hover:text-(--color-foreground) focus-visible:text-(--color-foreground) focus-visible:ring-1 focus-visible:ring-(--color-ring) disabled:cursor-not-allowed disabled:opacity-50"
                    >
                      {showPassword ? (
                        <EyeOff className="size-4" aria-hidden />
                      ) : (
                        <Eye className="size-4" aria-hidden />
                      )}
                    </button>
                  </div>
                  {fieldErrors.password && (
                    <p id="password-error" className="text-sm text-(--color-destructive)">
                      {fieldErrors.password}
                    </p>
                  )}
                </div>

                {loginSources.length > 0 && (
                  <div className="flex flex-col gap-1.5">
                    <Label htmlFor="login_source">{t("auth.auth_source")}</Label>
                    <Select
                      value={String(loginSource)}
                      onValueChange={(v) => setLoginSource(Number(v))}
                      disabled={submitting}
                    >
                      <SelectTrigger id="login_source" tabIndex={4}>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="0">{t("auth.local")}</SelectItem>
                        {loginSources.map((s) => (
                          <SelectItem key={s.id} value={String(s.id)}>
                            {s.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                )}

                {captchaEnabled && (
                  <Captcha
                    inputRef={captchaRef}
                    value={captcha}
                    onChange={setCaptcha}
                    onRefresh={refreshCaptcha}
                    refreshKey={captchaRefresh}
                    disabled={submitting}
                    invalid={"captcha" in fieldErrors}
                    describedBy={fieldErrors.captcha ? "captcha-error" : undefined}
                    imageTabIndex={5}
                    inputTabIndex={6}
                  >
                    {fieldErrors.captcha && (
                      <p id="captcha-error" className="text-sm text-(--color-destructive)">
                        {fieldErrors.captcha}
                      </p>
                    )}
                  </Captcha>
                )}

                <div className="mt-2 flex flex-col gap-3">
                  <Button type="submit" disabled={submitting || countingDown} tabIndex={7} className="w-full">
                    {submitLabel}
                  </Button>
                  <Button variant="link" size="inline" asChild className="self-center">
                    <a
                      href={subUrl("/user/sign-up")}
                      tabIndex={submitting ? -1 : 9}
                      aria-disabled={submitting || undefined}
                      className={submitting ? "pointer-events-none opacity-50" : undefined}
                      onClick={(e) => {
                        if (submitting) e.preventDefault();
                      }}
                    >
                      {t("auth.sign_up_now")}
                    </a>
                  </Button>
                </div>
              </div>
            </fieldset>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
