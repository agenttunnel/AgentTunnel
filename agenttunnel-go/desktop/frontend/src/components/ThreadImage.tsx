import { memo, useEffect, useState } from "react";
import { fetchImageBlob, type ImageRef } from "../lib/rpc";

type ThreadImageProps = {
  httpUrl: string;
  token: string;
  threadId: string;
  image: ImageRef;
};

/**
 * ThreadImage renders one image attached to a message: the bytes are fetched
 * over HTTP into a blob objectURL (revoked on unmount), shown as a fixed-width
 * thumbnail that opens full size on click. No image bytes ever enter React
 * state — only the objectURL string.
 */
export const ThreadImage = memo(function ThreadImage({
  httpUrl,
  token,
  threadId,
  image,
}: ThreadImageProps) {
  const [url, setUrl] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [full, setFull] = useState(false);

  useEffect(() => {
    let stale = false;
    let revoke: string | null = null;
    fetchImageBlob(httpUrl, token, threadId, image.id)
      .then((blob) => {
        if (stale) return;
        revoke = URL.createObjectURL(blob);
        setUrl(revoke);
      })
      .catch((e) => {
        if (!stale) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      stale = true;
      if (revoke !== null) URL.revokeObjectURL(revoke);
    };
  }, [httpUrl, token, threadId, image.id]);

  // The full-size overlay reuses the thumbnail's objectURL; Escape closes it.
  useEffect(() => {
    if (!full) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setFull(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [full]);

  if (error !== null) {
    return (
      <span className="thread-img thread-img-error" title={error}>
        image unavailable
      </span>
    );
  }
  if (url === null) {
    return <span className="thread-img thread-img-loading" aria-label="Loading image" />;
  }
  return (
    <>
      <img
        className="thread-img"
        src={url}
        alt={image.name ?? "attached image"}
        loading="lazy"
        onClick={() => setFull(true)}
      />
      {full && (
        <div className="img-overlay" onClick={() => setFull(false)} role="dialog" aria-label="Image preview">
          <img src={url} alt={image.name ?? "attached image"} />
        </div>
      )}
    </>
  );
});
