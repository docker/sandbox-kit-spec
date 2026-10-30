# compose `image: wordpress`, as content: the official image is the base
# and kind: workload exports the whole thing. The release is pinned by the
# build arg the descriptor validates and expands into its provide.
#
# The public image rather than the hardened one on purpose: dhi.io ships
# WordPress as php-fpm only, and this kit pins an -apache tag because the
# compose service it migrates expects one container serving HTTP.
ARG WP_VERSION=7.1.4
FROM wordpress:${WP_VERSION}-apache
