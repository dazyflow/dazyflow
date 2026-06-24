import { useRef, useState } from "react";
import type { ReactNode } from "react";
import { Upload, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { fileToIconDataURL, isImageIcon } from "../lib/iconImage";
import { Button } from "./Button";

// IconUpload is the shared "set a real icon" control used by flow
// settings and org settings. Shows a preview tile (the uploaded image,
// or a caller-supplied fallback when none is set), an Upload button that
// accepts SVG/PNG and stores it as a data: URL via onChange, and a
// Remove button. Errors (wrong type / too large) render inline.
export function IconUpload({
  value,
  onChange,
  fallback,
}: {
  value?: string;
  onChange: (next: string | undefined) => void;
  // Rendered in the preview tile when there's no uploaded image (e.g. a
  // lucide glyph for a flow, an initial for an org).
  fallback?: ReactNode;
}) {
  const { t } = useTranslation();
  const inputRef = useRef<HTMLInputElement | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const pick = () => inputRef.current?.click();

  const onFile = async (file: File | undefined) => {
    if (!file) return;
    setErr(null);
    try {
      onChange(await fileToIconDataURL(file));
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const hasImage = isImageIcon(value);

  return (
    <div className="icon-upload">
      <div className="icon-upload-preview" aria-hidden>
        {hasImage ? <img src={value} alt="" draggable={false} /> : fallback}
      </div>
      <div className="icon-upload-actions">
        <input
          ref={inputRef}
          type="file"
          accept="image/svg+xml,image/png,.svg,.png"
          hidden
          onChange={(e) => {
            void onFile(e.target.files?.[0]);
            // Reset so re-picking the same file fires change again.
            e.target.value = "";
          }}
        />
        <Button variant="ghost" onClick={pick}>
          <Upload size={14} /> {t("iconUpload.upload")}
        </Button>
        {hasImage && (
          <Button
            variant="ghost"
            size="icon"
            className="danger"
            onClick={() => onChange(undefined)}
            aria-label={t("iconUpload.remove")}
            title={t("iconUpload.remove")}
          >
            <Trash2 size={14} />
          </Button>
        )}
      </div>
      {err && <div className="icon-upload-error">{err}</div>}
    </div>
  );
}
