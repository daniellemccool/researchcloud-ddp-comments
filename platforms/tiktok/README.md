# TikTok

On TikTok the desk opens each student's video in their own streamed Firefox without any login, and the mitm capture keeps only the `/api/comment/list` responses, reduced to the comment fields listed in `platform.json`. `userContent.css` hides videos and blurs images in the student session so nobody has to watch the content. The safety copy records only the comment fields, never the media, together with the problem code the student picked (unavailable, private, login wall, region, captcha) for each video.

The values of `keep_cookies` entries ship verbatim in a public release template, so a platform definition must list only consent/preference cookies and never session or identifier cookies.
