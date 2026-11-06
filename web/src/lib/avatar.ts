/** googlePicture is a member's Google avatar, proxied by the control plane (auth.PicturePath). */
export function googlePicture(login: string): string {
  return `/auth/picture?login=${encodeURIComponent(login)}`;
}
