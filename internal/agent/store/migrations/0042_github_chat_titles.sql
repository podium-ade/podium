-- A pull request's chat was named from the first thing recorded in it, which for a review
-- Podium started itself is "alice opened https://github.com/acme/api/pull/12 for review":
-- every row in the list read the same. The conductor now names it from the pull request
-- (ghreview.ChatTitle): "#12: <its title>". This renames the chats it named before, as far
-- as the source key alone can — "#12" — and the next event on the pull request adds its
-- title. A title somebody set in Podium (auto_title off) is left alone.
update chats
   set title = '#' || split_part(source_key, '#', 2)
 where origin = 'github'
   and auto_title
   and source_key ~ '^github:[^/]+/[^#]+#[0-9]+$';
