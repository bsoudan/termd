{ nxterm }:

{ config, lib, pkgs, ... }:

let
  cfg = config.services.nxtermd;
  pkg = nxterm.packages.${pkgs.system}.default;
in
{
  options.services.nxtermd = {
    enable = lib.mkEnableOption "nxtermd terminal multiplexer server";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkg;
      defaultText = lib.literalExpression "nxterm.packages.\${pkgs.system}.default";
      description = "The nxtermd package to use.";
    };

    listen = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "unix:\${XDG_RUNTIME_DIR}/nxtermd.sock" "tcp://localhost:9100" ];
      description = ''
        Listen specs for the server. If empty, the server uses its
        config file default (unix:$XDG_RUNTIME_DIR/nxtermd.sock).
      '';
    };

    debug = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable debug logging.";
    };

    ssh = {
      hostKey = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Path to SSH host key file (auto-generated if missing).";
      };

      authorizedKeys = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Path to SSH authorized_keys file.";
      };

      noAuth = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Disable SSH authentication (insecure).";
      };
    };

    extraArgs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Extra command-line arguments passed to nxtermd.";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    systemd.user.services.nxtermd = {
      Unit = {
        Description = "nxtermd terminal multiplexer server";
      };

      Service = {
        Type = "notify";
        NotifyAccess = "all";
        ExecStart =
          let
            args = lib.concatStringsSep " " (
              lib.optional cfg.debug "--debug"
              ++ lib.optionals (cfg.ssh.hostKey != null) [ "--ssh-host-key" cfg.ssh.hostKey ]
              ++ lib.optionals (cfg.ssh.authorizedKeys != null) [ "--ssh-auth-keys" cfg.ssh.authorizedKeys ]
              ++ lib.optional cfg.ssh.noAuth "--ssh-no-auth"
              ++ cfg.extraArgs
              ++ cfg.listen
            );
          in
          "${cfg.package}/bin/nxtermd${lib.optionalString (args != "") " ${args}"}";
        ExecReload = "${pkgs.coreutils}/bin/kill -USR2 $MAINPID";
        Restart = "on-failure";
        RestartSec = 5;
      };

      Install = {
        WantedBy = [ "default.target" ];
      };
    };
  };
}
