# Claude Code in Container (`ccic`)

In all my projects I always run claude code in roughly the same way: I boot up a docker container based on ubuntu, install some packages, including claude code, mount the projects file system in the container and then run claude in the container. I would like to generalise this into a simple tool that does this for any project work.

## Behaviour

The tool should spin up 2 containers - a postgres container (option) and a container based on ubuntu running claude. The postgres container is only available to the claude container for running builds etc

- `ccic init`
  - setup the current folder as sandbox for ccic
  - ask for a prefix for this project
  - map the host folder to to `/${prefix}-workspace
  - ask if the project needs a postgres container and if so which version. 16 through to current
  - store all configuration in a `.ccic.conf` file in the root of the project. This may be committed into git but is not required.
- `ccic build`
  - build or rebuild the new container
  - look for a mise.toml file in the root and install the tools in the file
- `ccic destroy`
  - remove the containers, images and volumes related to this project
- `ccic force-rebuild`
  - same as destroy above and then build with docker's --no-cache
- `ccic start`
  - start the container, and then run claude
  - fail if init or build has not been run

the princple here is to allow me to control when to rebuild things manually - currently this tool is just for myself, if I roll it out to my team I may improve the dx more (e.g. start would run init and build if necesasry)


## CLAUDE.md
Somehow include a CLAUDE.md file in the project that explains how ccic works, and what is available to claude running in the container

## Tools to install

Claude code
https://mise.jdx.dev/
PLaywright (for head less browser testing)

The type of tooling we would install via mise would be: ruby, python, bun, node (not so much)


### Other Ubunutu packages

build-essential \
git \
curl \
unzip \
ca-certificates \
libpq-dev \
postgresql-client \
libyaml-dev \
libvips \
pkg-config \


# Headless-browser runtime (Playwright-managed Chromium; the `chromium`
# apt package on 24.04 is a snap stub and installs nothing usable).
# Confirm these again
libasound2t64 \
libatk1.0-0t64 \
libatk-bridge2.0-0t64 \
libatspi2.0-0t64 \
libcups2t64 \
libdrm2 \
libgbm1 \
libxcomposite1 \
libxdamage1 \
libxfixes3 \
libxkbcommon0 \
libxrandr2 \
# Fonts — without these, rendered text is boxes, which makes screenshots
# useless for visual checks.
fonts-liberation \
fonts-noto-color-emoji \
fonts-unifont \

## Stack

I would like this to be deployed as a single binary. This repo will be used to build the tool, but ultimately i'd like to be able to build a binary, push it to the releases page on GitHub and then distribute it / download it from there.
