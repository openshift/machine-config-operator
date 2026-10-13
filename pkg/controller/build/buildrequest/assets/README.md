# assets

These files get embedded within the Go binary and are not intended for direct
use. In particular, the Dockerfile is interspersed with Go templates and will
not build unless rendered with a tool such as [Gomplate](https://github.com/hairyhenderson/gomplate). 
